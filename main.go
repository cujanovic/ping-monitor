package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-ping/ping"
	brevo "github.com/getbrevo/brevo-go/lib"
)

// Config represents the configuration structure
type Config struct {
	PingIntervalSeconds  int      `json:"ping_interval_seconds"`
	PingCount            int      `json:"ping_count"`
	PingTimeThresholdMs  int      `json:"ping_time_threshold_ms"`
	Email                Email    `json:"email"`
	Targets              []Target `json:"targets"`
}

type Email struct {
	APIKey string `json:"api_key"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type Target struct {
	Name       string `json:"name"`
	TargetAddr string `json:"target"`
}

// PingMonitor handles the monitoring logic
type PingMonitor struct {
	config       Config
	downTargets  map[string]bool      // Track which targets are currently down
	downSince    map[string]time.Time // Track when targets went down
	slowTargets  map[string]bool      // Track which targets have high latency
	brevoClient  *brevo.APIClient
}

func NewPingMonitor(config Config) *PingMonitor {
	// Initialize Brevo client
	cfg := brevo.NewConfiguration()
	cfg.AddDefaultHeader("api-key", config.Email.APIKey)
	brevoClient := brevo.NewAPIClient(cfg)

	return &PingMonitor{
		config:      config,
		downTargets: make(map[string]bool),
		downSince:   make(map[string]time.Time),
		slowTargets: make(map[string]bool),
		brevoClient: brevoClient,
	}
}

// isIPAddress checks if a string is an IP address (IPv4 or IPv6)
func isIPAddress(addr string) bool {
	return net.ParseIP(addr) != nil
}

// getTargetLabel returns "IP" or "Domain" based on the target address type
func getTargetLabel(addr string) string {
	if isIPAddress(addr) {
		return "IP"
	}
	return "Domain"
}

// formatTargetInfo formats target info as "name (IP: x.x.x.x)" or "name (Domain: example.com)"
func formatTargetInfo(target Target) string {
	label := getTargetLabel(target.TargetAddr)
	return fmt.Sprintf("%s (%s: %s)", target.Name, label, target.TargetAddr)
}

// pingTarget pings a single target and returns success status and average RTT in milliseconds
func (pm *PingMonitor) pingTarget(target Target) (bool, float64) {
	pinger, err := ping.NewPinger(target.TargetAddr)
	if err != nil {
		log.Printf("Error creating pinger for %s: %v", formatTargetInfo(target), err)
		return false, 0
	}

	pinger.Count = pm.config.PingCount
	pinger.Timeout = 10 * time.Second
	pinger.SetPrivileged(false) // Use unprivileged ping

	err = pinger.Run()
	if err != nil {
		log.Printf("Error pinging %s: %v", formatTargetInfo(target), err)
		return false, 0
	}

	stats := pinger.Statistics()
	success := stats.PacketsRecv > 0
	avgRttMs := float64(stats.AvgRtt) / float64(time.Millisecond)
	
	if success {
		log.Printf("✓ %s - %d/%d packets received, avg %.2fms", 
			formatTargetInfo(target), stats.PacketsRecv, pm.config.PingCount, avgRttMs)
	} else {
		log.Printf("✗ %s - 0/%d packets received", formatTargetInfo(target), pm.config.PingCount)
	}

	return success, avgRttMs
}

// formatDuration formats a duration in a human-readable and precise way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		// Show milliseconds for very short durations
		if d < time.Second {
			return fmt.Sprintf("%d milliseconds", d.Milliseconds())
		}
		// Show seconds with decimal precision for under 1 minute
		return fmt.Sprintf("%.1f seconds", d.Seconds())
	} else if d < time.Hour {
		// Show minutes and seconds precisely
		minutes := int(d.Minutes())
		seconds := d.Seconds() - float64(minutes*60)
		return fmt.Sprintf("%d minutes %.0f seconds", minutes, seconds)
	} else if d < 24*time.Hour {
		// Show hours, minutes, and seconds for under 24 hours
		hours := int(d.Hours())
		remainingMinutes := d.Minutes() - float64(hours*60)
		minutes := int(remainingMinutes)
		seconds := d.Seconds() - float64(hours*3600) - float64(minutes*60)
		if minutes == 0 && seconds == 0 {
			return fmt.Sprintf("%d hours", hours)
		} else if seconds == 0 {
			return fmt.Sprintf("%d hours %d minutes", hours, minutes)
		}
		return fmt.Sprintf("%d hours %d minutes %.0f seconds", hours, minutes, seconds)
	} else {
		// Show days, hours, and minutes for over 24 hours
		days := int(d.Hours()) / 24
		remainingHours := d.Hours() - float64(days*24)
		hours := int(remainingHours)
		remainingMinutes := d.Minutes() - float64(days*24*60) - float64(hours*60)
		minutes := int(remainingMinutes)
		if hours == 0 && minutes == 0 {
			return fmt.Sprintf("%d days", days)
		} else if minutes == 0 {
			return fmt.Sprintf("%d days %d hours", days, hours)
		}
		return fmt.Sprintf("%d days %d hours %d minutes", days, hours, minutes)
	}
}

// sendEmail sends a notification email when a target goes down, recovers, or has high latency
func (pm *PingMonitor) sendEmail(target Target, alertType string, rttMs float64, downtime time.Duration) error {
	var subject, body string
	targetLabel := getTargetLabel(target.TargetAddr)
	
	switch alertType {
	case "down":
		subject = fmt.Sprintf("🔴 Ping Monitor Alert: %s is DOWN", target.Name)
		body = fmt.Sprintf(`
Ping Monitor Alert

Target: %s
%s: %s
Status: DOWN
Time: %s

This target is not responding to ping requests.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"))
	
	case "up":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s is UP", target.Name)
		
		// Format downtime in a human-readable way
		downtimeStr := formatDuration(downtime)
		
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: UP
Time: %s
Average RTT: %.2f ms
Downtime: %s

This target is now responding to ping requests.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), rttMs, downtimeStr)
	
	case "slow":
		subject = fmt.Sprintf("🟡 Ping Monitor Alert: %s has HIGH LATENCY", target.Name)
		body = fmt.Sprintf(`
Ping Monitor Alert

Target: %s
%s: %s
Status: HIGH LATENCY
Time: %s
Average RTT: %.2f ms
Threshold: %d ms

This target is responding but with high latency.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), rttMs, pm.config.PingTimeThresholdMs)
	
	case "normal":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s latency NORMAL", target.Name)
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: LATENCY NORMAL
Time: %s
Average RTT: %.2f ms
Threshold: %d ms

This target's latency has returned to normal.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), rttMs, pm.config.PingTimeThresholdMs)
	}

	// Create email using Brevo SDK
	email := brevo.SendSmtpEmail{
		Sender: &brevo.SendSmtpEmailSender{
			Name:  "Ping Monitor",
			Email: pm.config.Email.From,
		},
		To: []brevo.SendSmtpEmailTo{
			{
				Email: pm.config.Email.To,
			},
		},
		Subject: subject,
		HtmlContent: fmt.Sprintf("<pre>%s</pre>", body),
		TextContent: body,
	}

	ctx := context.Background()
	_, _, err := pm.brevoClient.TransactionalEmailsApi.SendTransacEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("failed to send email via Brevo: %v", err)
	}

	log.Printf("Email notification sent for %s", formatTargetInfo(target))
	return nil
}

// monitorTarget monitors a single target
func (pm *PingMonitor) monitorTarget(target Target) {
	success, rttMs := pm.pingTarget(target)
	
	// Check if status changed (down/up)
	wasDown := pm.downTargets[target.TargetAddr]
	
	if !success && !wasDown {
		// Target just went down - record the time
		pm.downTargets[target.TargetAddr] = true
		pm.downSince[target.TargetAddr] = time.Now()
		log.Printf("🔴 ALERT: %s is now DOWN", formatTargetInfo(target))
		if err := pm.sendEmail(target, "down", 0, 0); err != nil {
			log.Printf("Failed to send down notification for %s: %v", target.Name, err)
		}
	} else if success && wasDown {
		// Target just came back up - calculate downtime
		downtime := time.Since(pm.downSince[target.TargetAddr])
		delete(pm.downTargets, target.TargetAddr)
		delete(pm.downSince, target.TargetAddr)
		log.Printf("🟢 RECOVERY: %s is now UP (was down for %s)", formatTargetInfo(target), formatDuration(downtime))
		if err := pm.sendEmail(target, "up", rttMs, downtime); err != nil {
			log.Printf("Failed to send recovery notification for %s: %v", target.Name, err)
		}
	}
	
	// Check latency threshold (only if target is up and threshold is configured)
	if success && pm.config.PingTimeThresholdMs > 0 {
		wasSlow := pm.slowTargets[target.TargetAddr]
		isSlow := rttMs > float64(pm.config.PingTimeThresholdMs)
		
		if isSlow && !wasSlow {
			// Target just became slow
			pm.slowTargets[target.TargetAddr] = true
			log.Printf("🟡 ALERT: %s has HIGH LATENCY (%.2fms > %dms)", 
				formatTargetInfo(target), rttMs, pm.config.PingTimeThresholdMs)
			if err := pm.sendEmail(target, "slow", rttMs, 0); err != nil {
				log.Printf("Failed to send high latency notification for %s: %v", target.Name, err)
			}
		} else if !isSlow && wasSlow {
			// Latency returned to normal
			delete(pm.slowTargets, target.TargetAddr)
			log.Printf("🟢 RECOVERY: %s latency is now NORMAL (%.2fms <= %dms)", 
				formatTargetInfo(target), rttMs, pm.config.PingTimeThresholdMs)
			if err := pm.sendEmail(target, "normal", rttMs, 0); err != nil {
				log.Printf("Failed to send latency recovery notification for %s: %v", target.Name, err)
			}
		}
	}
}

// Start begins the monitoring process with distributed ping checks
func (pm *PingMonitor) Start() {
	numTargets := len(pm.config.Targets)
	if numTargets == 0 {
		log.Fatal("No targets configured")
		return
	}

	// Shuffle targets to randomize order on each startup
	targets := make([]Target, len(pm.config.Targets))
	copy(targets, pm.config.Targets)
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(targets), func(i, j int) {
		targets[i], targets[j] = targets[j], targets[i]
	})
	log.Printf("Targets shuffled for randomized monitoring order")

	// Calculate delay between checks to distribute pings evenly
	intervalSeconds := time.Duration(pm.config.PingIntervalSeconds) * time.Second
	delayBetweenTargets := intervalSeconds / time.Duration(numTargets)

	log.Printf("Starting ping monitor with %d targets, checking every %d seconds", 
		numTargets, pm.config.PingIntervalSeconds)
	log.Printf("Distributing pings with %v delay between targets for continuous monitoring", 
		delayBetweenTargets)

	// Start a goroutine for each target with staggered start times
	for i, target := range targets {
		// Calculate initial delay for this target to spread them out
		initialDelay := time.Duration(i) * delayBetweenTargets
		
		go func(t Target, delay time.Duration) {
			// Wait for initial stagger
			time.Sleep(delay)
			
			// Initial check
			pm.monitorTarget(t)
			
			// Create ticker for this specific target
			ticker := time.NewTicker(intervalSeconds)
			defer ticker.Stop()
			
			// Periodic checks for this target
			for range ticker.C {
				pm.monitorTarget(t)
			}
		}(target, initialDelay)
	}

	// Keep main goroutine running
	select {}
}

func loadConfig(filename string) (Config, error) {
	var config Config
	
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %v", err)
	}
	
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("failed to parse config file: %v", err)
	}
	
	return config, nil
}

func main() {
	// Load configuration
	config, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Validate configuration
	if len(config.Targets) == 0 {
		log.Fatal("No targets configured")
	}
	
	if config.PingIntervalSeconds <= 0 {
		log.Fatal("Ping interval must be greater than 0")
	}
	
	// Set default ping count if not specified
	if config.PingCount <= 0 {
		config.PingCount = 3
		log.Printf("Ping count not specified, using default: %d", config.PingCount)
	}

	// Create monitor
	monitor := NewPingMonitor(config)

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		log.Println("Shutting down ping monitor...")
		os.Exit(0)
	}()

	// Start monitoring
	monitor.Start()
}
