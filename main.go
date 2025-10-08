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
	"sync"
	"syscall"
	"time"

	"github.com/go-ping/ping"
	brevo "github.com/getbrevo/brevo-go/lib"
)

// Config represents the configuration structure
type Config struct {
	PingIntervalSeconds       int           `json:"ping_interval_seconds"`
	PingCount                 int           `json:"ping_count"`
	PingTimeThresholdMs       int           `json:"ping_time_threshold_ms"`
	PacketLossThresholdPercent int          `json:"packet_loss_threshold_percent"`
	AlertCooldownMinutes      int           `json:"alert_cooldown_minutes"`
	EmailRateLimitPerHour     int           `json:"email_rate_limit_per_hour"`
	MaxConcurrentPings        int           `json:"max_concurrent_pings"`
	Email                     Email         `json:"email"`
	Targets                   []Target      `json:"targets"`
}

type Email struct {
	APIKey string `json:"api_key"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type Target struct {
	Name                       string `json:"name"`
	TargetAddr                 string `json:"target"`
	PingThresholdMs            int    `json:"ping_time_threshold_ms,omitempty"`
	PacketLossThresholdPercent int    `json:"packet_loss_threshold_percent,omitempty"`
}

// AlertKey uniquely identifies an alert type for a target
type AlertKey struct {
	TargetAddr string
	AlertType  string
}

// PingMonitor handles the monitoring logic
type PingMonitor struct {
	config              Config
	downTargets         map[string]bool      // Track which targets are currently down
	downSince           map[string]time.Time // Track when targets went down
	slowTargets         map[string]bool      // Track which targets have high latency
	packetLossTargets   map[string]bool      // Track which targets have packet loss
	lastAlertTime       map[AlertKey]time.Time // Track last alert time for cooldown
	emailsSentThisHour  []time.Time          // Sliding window of email timestamps
	brevoClient         *brevo.APIClient
	mu                  sync.RWMutex         // Protect shared state
	emailMu             sync.Mutex           // Protect email rate limiting
	semaphore           chan struct{}        // Limit concurrent pings
}

func NewPingMonitor(config Config) *PingMonitor {
	// Initialize Brevo client
	cfg := brevo.NewConfiguration()
	cfg.AddDefaultHeader("api-key", config.Email.APIKey)
	brevoClient := brevo.NewAPIClient(cfg)

	// Set defaults
	if config.PacketLossThresholdPercent == 0 {
		config.PacketLossThresholdPercent = 50 // Default 50% packet loss threshold
	}
	if config.AlertCooldownMinutes == 0 {
		config.AlertCooldownMinutes = 15 // Default 15 minutes cooldown
	}
	if config.EmailRateLimitPerHour == 0 {
		config.EmailRateLimitPerHour = 60 // Default 60 emails per hour
	}
	if config.MaxConcurrentPings == 0 {
		config.MaxConcurrentPings = 10 // Default 10 concurrent pings
	}

	// Create semaphore for concurrent ping limiting
	semaphore := make(chan struct{}, config.MaxConcurrentPings)

	return &PingMonitor{
		config:            config,
		downTargets:       make(map[string]bool),
		downSince:         make(map[string]time.Time),
		slowTargets:       make(map[string]bool),
		packetLossTargets: make(map[string]bool),
		lastAlertTime:     make(map[AlertKey]time.Time),
		emailsSentThisHour: make([]time.Time, 0),
		brevoClient:       brevoClient,
		semaphore:         semaphore,
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

// pingTarget pings a single target and returns success status, packet loss, and average RTT
func (pm *PingMonitor) pingTarget(target Target) (bool, int, float64) {
	pinger, err := ping.NewPinger(target.TargetAddr)
	if err != nil {
		log.Printf("Error creating pinger for %s: %v", formatTargetInfo(target), err)
		return false, 100, 0
	}

	pinger.Count = pm.config.PingCount
	pinger.Timeout = 10 * time.Second
	pinger.SetPrivileged(false) // Use unprivileged ping

	err = pinger.Run()
	if err != nil {
		log.Printf("Error pinging %s: %v", formatTargetInfo(target), err)
		return false, 100, 0
	}

	stats := pinger.Statistics()
	packetsRecv := stats.PacketsRecv
	packetsSent := stats.PacketsSent
	
	var packetLossPercent int
	if packetsSent > 0 {
		packetLossPercent = int(100 * (packetsSent - packetsRecv) / packetsSent)
	} else {
		packetLossPercent = 100
	}

	success := packetsRecv > 0
	avgRttMs := float64(stats.AvgRtt) / float64(time.Millisecond)
	
	if success {
		log.Printf("✓ %s - %d/%d packets received (%.0f%% loss), avg %.2fms", 
			formatTargetInfo(target), packetsRecv, packetsSent, float64(packetLossPercent), avgRttMs)
	} else {
		log.Printf("✗ %s - 0/%d packets received (100%% loss)", 
			formatTargetInfo(target), packetsSent)
	}

	return success, packetLossPercent, avgRttMs
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

// canSendAlert checks if an alert can be sent based on cooldown and rate limiting
func (pm *PingMonitor) canSendAlert(target Target, alertType string) bool {
	pm.mu.RLock()
	key := AlertKey{TargetAddr: target.TargetAddr, AlertType: alertType}
	lastAlert, exists := pm.lastAlertTime[key]
	pm.mu.RUnlock()

	// Check cooldown
	if exists {
		cooldownDuration := time.Duration(pm.config.AlertCooldownMinutes) * time.Minute
		if time.Since(lastAlert) < cooldownDuration {
			log.Printf("⏱️  Alert cooldown active for %s (%s) - suppressing duplicate alert", 
				formatTargetInfo(target), alertType)
			return false
		}
	}

	// Check rate limit
	pm.emailMu.Lock()
	defer pm.emailMu.Unlock()

	// Remove emails older than 1 hour from the sliding window
	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)
	validEmails := make([]time.Time, 0)
	for _, t := range pm.emailsSentThisHour {
		if t.After(oneHourAgo) {
			validEmails = append(validEmails, t)
		}
	}
	pm.emailsSentThisHour = validEmails

	// Check if we're at the rate limit
	if len(pm.emailsSentThisHour) >= pm.config.EmailRateLimitPerHour {
		log.Printf("⚠️  Email rate limit reached (%d/hour) - suppressing alert for %s", 
			pm.config.EmailRateLimitPerHour, formatTargetInfo(target))
		return false
	}

	return true
}

// recordAlert records that an alert was sent
func (pm *PingMonitor) recordAlert(target Target, alertType string) {
	now := time.Now()
	
	pm.mu.Lock()
	key := AlertKey{TargetAddr: target.TargetAddr, AlertType: alertType}
	pm.lastAlertTime[key] = now
	pm.mu.Unlock()

	pm.emailMu.Lock()
	pm.emailsSentThisHour = append(pm.emailsSentThisHour, now)
	pm.emailMu.Unlock()
}

// sendEmail sends a notification email when a target goes down, recovers, or has high latency
func (pm *PingMonitor) sendEmail(target Target, alertType string, rttMs float64, packetLoss int, downtime time.Duration) error {
	var subject, body string
	targetLabel := getTargetLabel(target.TargetAddr)
	threshold := pm.getTargetThreshold(target)
	
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
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), rttMs, threshold)
	
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
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), rttMs, threshold)
	
	case "packet_loss":
		packetLossThreshold := pm.getPacketLossThreshold(target)
		subject = fmt.Sprintf("🟠 Ping Monitor Alert: %s has PACKET LOSS", target.Name)
		body = fmt.Sprintf(`
Ping Monitor Alert

Target: %s
%s: %s
Status: PACKET LOSS
Time: %s
Packet Loss: %d%%
Threshold: %d%%

This target is experiencing significant packet loss.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), packetLoss, packetLossThreshold)
	
	case "packet_loss_normal":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s packet loss NORMAL", target.Name)
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: PACKET LOSS NORMAL
Time: %s
Packet Loss: %d%%

This target's packet loss has returned to normal levels.
`, target.Name, targetLabel, target.TargetAddr, time.Now().Format("2006-01-02 15:04:05"), packetLoss)
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

	log.Printf("📧 Email notification sent for %s (%s)", formatTargetInfo(target), alertType)
	return nil
}

// getTargetThreshold returns the effective threshold for a target
// Uses per-target threshold if set, otherwise falls back to global threshold or 200ms default
func (pm *PingMonitor) getTargetThreshold(target Target) int {
	if target.PingThresholdMs > 0 {
		return target.PingThresholdMs
	}
	if pm.config.PingTimeThresholdMs > 0 {
		return pm.config.PingTimeThresholdMs
	}
	return 200 // Default threshold
}

// getPacketLossThreshold returns the effective packet loss threshold for a target
func (pm *PingMonitor) getPacketLossThreshold(target Target) int {
	if target.PacketLossThresholdPercent > 0 {
		return target.PacketLossThresholdPercent
	}
	if pm.config.PacketLossThresholdPercent > 0 {
		return pm.config.PacketLossThresholdPercent
	}
	return 50 // Default 50% packet loss threshold
}

// monitorTarget monitors a single target
func (pm *PingMonitor) monitorTarget(target Target) {
	// Acquire semaphore to limit concurrent pings
	pm.semaphore <- struct{}{}
	defer func() { <-pm.semaphore }()

	success, packetLoss, rttMs := pm.pingTarget(target)
	
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	// Check if status changed (down/up)
	wasDown := pm.downTargets[target.TargetAddr]
	
	if !success && !wasDown {
		// Target just went down - record the time
		pm.downTargets[target.TargetAddr] = true
		pm.downSince[target.TargetAddr] = time.Now()
		log.Printf("🔴 ALERT: %s is now DOWN", formatTargetInfo(target))
		
		if pm.canSendAlert(target, "down") {
			pm.mu.Unlock() // Unlock before sending email
			if err := pm.sendEmail(target, "down", 0, packetLoss, 0); err != nil {
				log.Printf("Failed to send down notification for %s: %v", target.Name, err)
			} else {
				pm.recordAlert(target, "down")
			}
			pm.mu.Lock() // Re-lock after email
		}
	} else if success && wasDown {
		// Target just came back up - calculate downtime
		downtime := time.Since(pm.downSince[target.TargetAddr])
		delete(pm.downTargets, target.TargetAddr)
		delete(pm.downSince, target.TargetAddr)
		log.Printf("🟢 RECOVERY: %s is now UP (was down for %s)", formatTargetInfo(target), formatDuration(downtime))
		
		if pm.canSendAlert(target, "up") {
			pm.mu.Unlock() // Unlock before sending email
			if err := pm.sendEmail(target, "up", rttMs, packetLoss, downtime); err != nil {
				log.Printf("Failed to send recovery notification for %s: %v", target.Name, err)
			} else {
				pm.recordAlert(target, "up")
			}
			pm.mu.Lock() // Re-lock after email
		}
	}
	
	// Check packet loss threshold (only if target is up)
	if success {
		packetLossThreshold := pm.getPacketLossThreshold(target)
		hadPacketLoss := pm.packetLossTargets[target.TargetAddr]
		hasPacketLoss := packetLoss >= packetLossThreshold
		
		if hasPacketLoss && !hadPacketLoss {
			// Packet loss just exceeded threshold
			pm.packetLossTargets[target.TargetAddr] = true
			log.Printf("🟠 ALERT: %s has PACKET LOSS (%d%% >= %d%%)", 
				formatTargetInfo(target), packetLoss, packetLossThreshold)
			
			if pm.canSendAlert(target, "packet_loss") {
				pm.mu.Unlock()
				if err := pm.sendEmail(target, "packet_loss", rttMs, packetLoss, 0); err != nil {
					log.Printf("Failed to send packet loss notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss")
				}
				pm.mu.Lock()
			}
		} else if !hasPacketLoss && hadPacketLoss {
			// Packet loss returned to normal
			delete(pm.packetLossTargets, target.TargetAddr)
			log.Printf("🟢 RECOVERY: %s packet loss is now NORMAL (%d%% < %d%%)", 
				formatTargetInfo(target), packetLoss, packetLossThreshold)
			
			if pm.canSendAlert(target, "packet_loss_normal") {
				pm.mu.Unlock()
				if err := pm.sendEmail(target, "packet_loss_normal", rttMs, packetLoss, 0); err != nil {
					log.Printf("Failed to send packet loss recovery notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss_normal")
				}
				pm.mu.Lock()
			}
		}
		
		// Check latency threshold (only if target is up and no major packet loss)
		threshold := pm.getTargetThreshold(target)
		wasSlow := pm.slowTargets[target.TargetAddr]
		isSlow := rttMs > float64(threshold)
		
		if isSlow && !wasSlow {
			// Target just became slow
			pm.slowTargets[target.TargetAddr] = true
			log.Printf("🟡 ALERT: %s has HIGH LATENCY (%.2fms > %dms)", 
				formatTargetInfo(target), rttMs, threshold)
			
			if pm.canSendAlert(target, "slow") {
				pm.mu.Unlock()
				if err := pm.sendEmail(target, "slow", rttMs, packetLoss, 0); err != nil {
					log.Printf("Failed to send high latency notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "slow")
				}
				pm.mu.Lock()
			}
		} else if !isSlow && wasSlow {
			// Latency returned to normal
			delete(pm.slowTargets, target.TargetAddr)
			log.Printf("🟢 RECOVERY: %s latency is now NORMAL (%.2fms <= %dms)", 
				formatTargetInfo(target), rttMs, threshold)
			
			if pm.canSendAlert(target, "normal") {
				pm.mu.Unlock()
				if err := pm.sendEmail(target, "normal", rttMs, packetLoss, 0); err != nil {
					log.Printf("Failed to send latency recovery notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "normal")
				}
				pm.mu.Lock()
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

	log.Printf("🚀 Starting Ping Monitor with the following settings:")
	log.Printf("   • Targets: %d", numTargets)
	log.Printf("   • Ping Interval: %d seconds", pm.config.PingIntervalSeconds)
	log.Printf("   • Ping Count: %d", pm.config.PingCount)
	log.Printf("   • Packet Loss Threshold: %d%%", pm.config.PacketLossThresholdPercent)
	log.Printf("   • Alert Cooldown: %d minutes", pm.config.AlertCooldownMinutes)
	log.Printf("   • Email Rate Limit: %d/hour", pm.config.EmailRateLimitPerHour)
	log.Printf("   • Max Concurrent Pings: %d", pm.config.MaxConcurrentPings)

	// Shuffle targets to randomize order on each startup
	targets := make([]Target, len(pm.config.Targets))
	copy(targets, pm.config.Targets)
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(targets), func(i, j int) {
		targets[i], targets[j] = targets[j], targets[i]
	})
	log.Printf("🔀 Targets shuffled for randomized monitoring order")

	// Calculate delay between checks to distribute pings evenly
	intervalSeconds := time.Duration(pm.config.PingIntervalSeconds) * time.Second
	delayBetweenTargets := intervalSeconds / time.Duration(numTargets)

	log.Printf("📊 Distributing pings with %v delay between targets for continuous monitoring", 
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

	log.Printf("✅ All monitoring goroutines started")

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
	log.Printf("🎯 Ping Monitor Service Starting...")
	
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
		log.Println("⏹️  Shutting down ping monitor...")
		os.Exit(0)
	}()

	// Start monitoring
	monitor.Start()
}
