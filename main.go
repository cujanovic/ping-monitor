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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-ping/ping"
	brevo "github.com/getbrevo/brevo-go/lib"
)

// Config represents the configuration structure
type Config struct {
	PingIntervalSeconds        int    `json:"ping_interval_seconds"`
	PingCount                  int    `json:"ping_count"`
	PingTimeThresholdMs        int    `json:"ping_time_threshold_ms"`
	PacketLossThresholdPercent int    `json:"packet_loss_threshold_percent"`
	AlertCooldownMinutes       int    `json:"alert_cooldown_minutes"`
	EmailRateLimitPerHour      int    `json:"email_rate_limit_per_hour"`
	MaxConcurrentPings         int    `json:"max_concurrent_pings"`
	DefaultTimeoutSeconds      int    `json:"default_timeout_seconds"`
	ReportTimeOffsetHours      int    `json:"report_time_offset_hours"` // Timezone offset for reports (e.g., +2 for UTC+2)
	SummaryReportEnabled       bool   `json:"summary_report_enabled"`
	SummaryReportSchedule      string `json:"summary_report_schedule"` // "daily" or "weekly"
	SummaryReportTime          string `json:"summary_report_time"`     // "HH:MM" format
	Email                      Email  `json:"email"`
	Targets                    []Target `json:"targets"`
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
	TimeoutSeconds             int    `json:"timeout_seconds,omitempty"`
}

// EventRecord tracks a single event occurrence
type EventRecord struct {
	Timestamp   time.Time
	EventType   string  // "down", "up", "high_latency", "latency_normal", "packet_loss", "packet_loss_normal"
	Value       float64 // latency in ms or packet loss percentage
	Threshold   float64 // threshold value
	Duration    time.Duration // for recovery events - how long the issue lasted
}

// TargetStats tracks statistics for a target
type TargetStats struct {
	TotalChecks       int64
	SuccessfulChecks  int64
	FailedChecks      int64
	TotalDowntime     time.Duration
	LastDowntime      time.Time
	TotalPacketLoss   int64
	TotalLatency      float64
	MinLatency        float64
	MaxLatency        float64
	MaxPacketLoss     int
	HighLatencyCount  int64
	PacketLossEvents  int64
	RecentEvents      []EventRecord // Store recent events for reporting
}

// AlertKey uniquely identifies an alert type for a target
type AlertKey struct {
	TargetAddr string
	AlertType  string
}

// PingMonitor handles the monitoring logic
type PingMonitor struct {
	config             Config
	downTargets        map[string]bool                // Track which targets are currently down
	downSince          map[string]time.Time           // Track when targets went down
	slowTargets        map[string]bool                // Track which targets have high latency
	slowSince          map[string]time.Time           // Track when targets became slow
	packetLossTargets  map[string]bool                // Track which targets have packet loss
	packetLossSince    map[string]time.Time           // Track when packet loss started
	lastAlertTime      map[AlertKey]time.Time         // Track last alert time for cooldown
	emailsSentThisHour []time.Time                    // Sliding window of email timestamps
	targetStats        map[string]*TargetStats        // Statistics per target
	statsStartTime     time.Time                      // When stats collection started
	brevoClient        *brevo.APIClient
	mu                 sync.RWMutex                   // Protect shared state
	emailMu            sync.Mutex                     // Protect email rate limiting
	semaphore          chan struct{}                  // Limit concurrent pings
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
	if config.DefaultTimeoutSeconds == 0 {
		config.DefaultTimeoutSeconds = 10 // Default 10 seconds timeout
	}

	// Create semaphore for concurrent ping limiting
	semaphore := make(chan struct{}, config.MaxConcurrentPings)

	// Initialize target stats
	targetStats := make(map[string]*TargetStats)
	for _, target := range config.Targets {
		targetStats[target.TargetAddr] = &TargetStats{
			MinLatency:   -1, // -1 indicates not set
			RecentEvents: make([]EventRecord, 0),
		}
	}

	return &PingMonitor{
		config:             config,
		downTargets:        make(map[string]bool),
		downSince:          make(map[string]time.Time),
		slowTargets:        make(map[string]bool),
		slowSince:          make(map[string]time.Time),
		packetLossTargets:  make(map[string]bool),
		packetLossSince:    make(map[string]time.Time),
		lastAlertTime:      make(map[AlertKey]time.Time),
		emailsSentThisHour: make([]time.Time, 0),
		targetStats:        targetStats,
		statsStartTime:     time.Now(),
		brevoClient:        brevoClient,
		semaphore:          semaphore,
	}
}

// ValidateConfig validates the configuration
func ValidateConfig(config Config) error {
	errors := make([]string, 0)

	// Validate basic settings
	if config.PingIntervalSeconds <= 0 {
		errors = append(errors, "ping_interval_seconds must be greater than 0")
	}
	if config.PingIntervalSeconds < 5 {
		errors = append(errors, "ping_interval_seconds should be at least 5 seconds for reliability")
	}
	if config.PingCount < 1 {
		errors = append(errors, "ping_count must be at least 1")
	}
	if config.PingCount > 10 {
		errors = append(errors, "ping_count should not exceed 10 for performance reasons")
	}

	// Validate thresholds
	if config.PingTimeThresholdMs < 0 {
		errors = append(errors, "ping_time_threshold_ms cannot be negative")
	}
	if config.PacketLossThresholdPercent < 0 || config.PacketLossThresholdPercent > 100 {
		errors = append(errors, "packet_loss_threshold_percent must be between 0 and 100")
	}
	if config.AlertCooldownMinutes < 0 {
		errors = append(errors, "alert_cooldown_minutes cannot be negative")
	}
	if config.EmailRateLimitPerHour < 1 {
		errors = append(errors, "email_rate_limit_per_hour must be at least 1")
	}
	if config.EmailRateLimitPerHour > 300 {
		errors = append(errors, "email_rate_limit_per_hour exceeds Brevo free tier limit (300/day)")
	}
	if config.MaxConcurrentPings < 1 {
		errors = append(errors, "max_concurrent_pings must be at least 1")
	}
	if config.MaxConcurrentPings > 50 {
		errors = append(errors, "max_concurrent_pings should not exceed 50 for stability")
	}
	if config.DefaultTimeoutSeconds < 1 || config.DefaultTimeoutSeconds > 60 {
		errors = append(errors, "default_timeout_seconds must be between 1 and 60")
	}

	// Validate email config
	if config.Email.APIKey == "" || config.Email.APIKey == "your-brevo-api-key-here" {
		errors = append(errors, "email.api_key must be configured with a valid Brevo API key")
	}
	if config.Email.From == "" {
		errors = append(errors, "email.from cannot be empty")
	}
	if !strings.Contains(config.Email.From, "@") {
		errors = append(errors, "email.from must be a valid email address")
	}
	if config.Email.To == "" {
		errors = append(errors, "email.to cannot be empty")
	}
	if !strings.Contains(config.Email.To, "@") {
		errors = append(errors, "email.to must be a valid email address")
	}

	// Validate summary report settings
	if config.SummaryReportEnabled {
		if config.SummaryReportSchedule != "daily" && config.SummaryReportSchedule != "weekly" {
			errors = append(errors, "summary_report_schedule must be 'daily' or 'weekly'")
		}
		if config.SummaryReportTime != "" {
			parts := strings.Split(config.SummaryReportTime, ":")
			if len(parts) != 2 {
				errors = append(errors, "summary_report_time must be in HH:MM format")
			}
		}
	}

	// Validate targets
	if len(config.Targets) == 0 {
		errors = append(errors, "at least one target must be configured")
	}
	if len(config.Targets) > 1000 {
		errors = append(errors, "maximum 1000 targets supported")
	}

	targetNames := make(map[string]bool)
	targetAddrs := make(map[string]bool)
	
	for i, target := range config.Targets {
		// Validate target name
		if target.Name == "" {
			errors = append(errors, fmt.Sprintf("target[%d].name cannot be empty", i))
		}
		if targetNames[target.Name] {
			errors = append(errors, fmt.Sprintf("duplicate target name: %s", target.Name))
		}
		targetNames[target.Name] = true

		// Validate target address
		if target.TargetAddr == "" {
			errors = append(errors, fmt.Sprintf("target[%d].target cannot be empty", i))
		}
		if targetAddrs[target.TargetAddr] {
			errors = append(errors, fmt.Sprintf("duplicate target address: %s", target.TargetAddr))
		}
		targetAddrs[target.TargetAddr] = true

		// Validate per-target thresholds
		if target.PingThresholdMs < 0 {
			errors = append(errors, fmt.Sprintf("target[%d].ping_time_threshold_ms cannot be negative", i))
		}
		if target.PacketLossThresholdPercent < 0 || target.PacketLossThresholdPercent > 100 {
			errors = append(errors, fmt.Sprintf("target[%d].packet_loss_threshold_percent must be between 0 and 100", i))
		}
		if target.TimeoutSeconds < 0 || target.TimeoutSeconds > 60 {
			errors = append(errors, fmt.Sprintf("target[%d].timeout_seconds must be between 0 and 60", i))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
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

// getTargetTimeout returns the effective timeout for a target
func (pm *PingMonitor) getTargetTimeout(target Target) time.Duration {
	if target.TimeoutSeconds > 0 {
		return time.Duration(target.TimeoutSeconds) * time.Second
	}
	return time.Duration(pm.config.DefaultTimeoutSeconds) * time.Second
}

// pingTarget pings a single target and returns success status, packet loss, and average RTT
func (pm *PingMonitor) pingTarget(target Target) (bool, int, float64) {
	pinger, err := ping.NewPinger(target.TargetAddr)
	if err != nil {
		log.Printf("⚠️  Error creating pinger for %s: %v (gracefully continuing)", formatTargetInfo(target), err)
		return false, 100, 0
	}

	pinger.Count = pm.config.PingCount
	pinger.Timeout = pm.getTargetTimeout(target)
	pinger.SetPrivileged(false) // Use unprivileged ping

	err = pinger.Run()
	if err != nil {
		log.Printf("⚠️  Error pinging %s: %v (gracefully continuing)", formatTargetInfo(target), err)
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
	
	// Update statistics
	pm.updateTargetStats(target, success, packetLossPercent, avgRttMs)
	
	if success {
		log.Printf("✓ %s - %d/%d packets received (%.0f%% loss), avg %.2fms", 
			formatTargetInfo(target), packetsRecv, packetsSent, float64(packetLossPercent), avgRttMs)
	} else {
		log.Printf("✗ %s - 0/%d packets received (100%% loss)", 
			formatTargetInfo(target), packetsSent)
	}

	return success, packetLossPercent, avgRttMs
}

// updateTargetStats updates statistics for a target
func (pm *PingMonitor) updateTargetStats(target Target, success bool, packetLoss int, latencyMs float64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stats, exists := pm.targetStats[target.TargetAddr]
	if !exists {
		stats = &TargetStats{MinLatency: -1, RecentEvents: make([]EventRecord, 0)}
		pm.targetStats[target.TargetAddr] = stats
	}

	stats.TotalChecks++
	if success {
		stats.SuccessfulChecks++
		stats.TotalLatency += latencyMs
		
		// Update min/max latency
		if stats.MinLatency < 0 || latencyMs < stats.MinLatency {
			stats.MinLatency = latencyMs
		}
		if latencyMs > stats.MaxLatency {
			stats.MaxLatency = latencyMs
		}
		
		// Track high latency
		threshold := pm.getTargetThreshold(target)
		if latencyMs > float64(threshold) {
			stats.HighLatencyCount++
		}
	} else {
		stats.FailedChecks++
	}

	stats.TotalPacketLoss += int64(packetLoss)
	
	// Track max packet loss
	if packetLoss > stats.MaxPacketLoss {
		stats.MaxPacketLoss = packetLoss
	}
	
	// Track packet loss events
	packetLossThreshold := pm.getPacketLossThreshold(target)
	if packetLoss >= packetLossThreshold {
		stats.PacketLossEvents++
	}
}

// recordEvent records an event for summary reporting
func (pm *PingMonitor) recordEvent(target Target, eventType string, value float64, threshold float64, duration time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stats, exists := pm.targetStats[target.TargetAddr]
	if !exists {
		stats = &TargetStats{MinLatency: -1, RecentEvents: make([]EventRecord, 0)}
		pm.targetStats[target.TargetAddr] = stats
	}

	event := EventRecord{
		Timestamp: time.Now(),
		EventType: eventType,
		Value:     value,
		Threshold: threshold,
		Duration:  duration,
	}

	stats.RecentEvents = append(stats.RecentEvents, event)
	
	// Keep only the most recent 50 events to avoid unbounded growth
	if len(stats.RecentEvents) > 50 {
		stats.RecentEvents = stats.RecentEvents[len(stats.RecentEvents)-50:]
	}
}

// formatEvent formats an event record in a human-readable way
func formatEvent(event EventRecord) string {
	switch event.EventType {
	case "down":
		return "Target went DOWN"
	case "up":
		if event.Duration > 0 {
			return fmt.Sprintf("Target recovered (downtime: %s)", formatDuration(event.Duration))
		}
		return "Target recovered"
	case "packet_loss":
		return fmt.Sprintf("Packet loss: %.0f%% (threshold: %.0f%%)", event.Value, event.Threshold)
	case "packet_loss_normal":
		return fmt.Sprintf("Packet loss recovered: %.0f%%", event.Value)
	case "high_latency":
		return fmt.Sprintf("High latency: %.2fms (threshold: %.0fms)", event.Value, event.Threshold)
	case "latency_normal":
		return fmt.Sprintf("Latency recovered: %.2fms", event.Value)
	default:
		return fmt.Sprintf("%s: %.2f", event.EventType, event.Value)
	}
}

// getReportTime returns the current time adjusted by the configured offset
func (pm *PingMonitor) getReportTime() time.Time {
	return time.Now().Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
}

// formatNumber formats a number with commas for readability
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	str := fmt.Sprintf("%d", n)
	result := ""
	for i, digit := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(digit)
	}
	return result
}

// formatDuration formats a duration in a human-readable and precise way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		if d < time.Second {
			return fmt.Sprintf("%d milliseconds", d.Milliseconds())
		}
		return fmt.Sprintf("%.1f seconds", d.Seconds())
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := d.Seconds() - float64(minutes*60)
		return fmt.Sprintf("%d minutes %.0f seconds", minutes, seconds)
	} else if d < 24*time.Hour {
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

// sendEmail sends a notification email
func (pm *PingMonitor) sendEmail(target Target, alertType string, rttMs float64, packetLoss int, downtime time.Duration) error {
	var subject, body string
	targetLabel := getTargetLabel(target.TargetAddr)
	threshold := pm.getTargetThreshold(target)
	reportTime := pm.getReportTime()
	
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
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"))
	
	case "up":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s is UP", target.Name)
		downtimeStr := formatDuration(downtime)
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: UP
Time: %s
Average RTT: %.2f ms
Downtime Duration: %s

This target is now responding to ping requests.
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"), rttMs, downtimeStr)
	
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
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"), rttMs, threshold)
	
	case "normal":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s latency NORMAL", target.Name)
		durationStr := ""
		if downtime > 0 {
			durationStr = fmt.Sprintf("\nIncident Duration: %s", formatDuration(downtime))
		}
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: LATENCY NORMAL
Time: %s
Average RTT: %.2f ms
Threshold: %d ms%s

This target's latency has returned to normal.
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"), rttMs, threshold, durationStr)
	
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
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"), packetLoss, packetLossThreshold)
	
	case "packet_loss_normal":
		subject = fmt.Sprintf("🟢 Ping Monitor Recovery: %s packet loss NORMAL", target.Name)
		durationStr := ""
		if downtime > 0 {
			durationStr = fmt.Sprintf("\nIncident Duration: %s", formatDuration(downtime))
		}
		body = fmt.Sprintf(`
Ping Monitor Recovery

Target: %s
%s: %s
Status: PACKET LOSS NORMAL
Time: %s
Packet Loss: %d%%%s

This target's packet loss has returned to normal levels.
`, target.Name, targetLabel, target.TargetAddr, reportTime.Format("2006-01-02 15:04:05"), packetLoss, durationStr)
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
		Subject:     subject,
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

// TargetReport holds computed statistics for a target
type TargetReport struct {
	Target         Target
	Uptime         float64
	AvgLatency     float64
	MinLatency     float64
	MaxLatency     float64
	AvgPacketLoss  float64
	TotalIssues    int64
	Stats          *TargetStats
}

// sendSummaryReport sends a summary report email
func (pm *PingMonitor) sendSummaryReport() error {
	// Build the report body while holding the lock
	pm.mu.RLock()
	reportDuration := time.Since(pm.statsStartTime)
	schedule := pm.config.SummaryReportSchedule
	now := time.Now()
	reportStart := now.Add(-reportDuration)
	
	subject := fmt.Sprintf("📊 Ping Monitor %s Summary Report", strings.Title(schedule))
	
	// Calculate statistics for all targets and categorize them
	var healthyTargets []TargetReport
	var issueTargets []TargetReport
	var criticalTargets []TargetReport
	var totalChecks, successfulChecks int64
	var totalUptime float64
	targetCount := 0

	for _, target := range pm.config.Targets {
		stats, exists := pm.targetStats[target.TargetAddr]
		if !exists || stats.TotalChecks == 0 {
			continue
		}

		uptime := 100.0
		if stats.TotalChecks > 0 {
			uptime = (float64(stats.SuccessfulChecks) / float64(stats.TotalChecks)) * 100
		}

		avgLatency := 0.0
		minLatency := 0.0
		if stats.SuccessfulChecks > 0 {
			avgLatency = stats.TotalLatency / float64(stats.SuccessfulChecks)
			minLatency = stats.MinLatency
			if minLatency < 0 {
				minLatency = 0
			}
		}

		avgPacketLoss := 0.0
		if stats.TotalChecks > 0 {
			avgPacketLoss = float64(stats.TotalPacketLoss) / float64(stats.TotalChecks)
		}

		totalIssues := stats.HighLatencyCount + stats.PacketLossEvents + stats.FailedChecks

		report := TargetReport{
			Target:        target,
			Uptime:        uptime,
			AvgLatency:    avgLatency,
			MinLatency:    minLatency,
			MaxLatency:    stats.MaxLatency,
			AvgPacketLoss: avgPacketLoss,
			TotalIssues:   totalIssues,
			Stats:         stats,
		}

		// Categorize by uptime
		if uptime >= 99.0 {
			healthyTargets = append(healthyTargets, report)
		} else if uptime >= 95.0 {
			issueTargets = append(issueTargets, report)
		} else {
			criticalTargets = append(criticalTargets, report)
		}

		totalChecks += stats.TotalChecks
		successfulChecks += stats.SuccessfulChecks
		totalUptime += uptime
		targetCount++
	}

	// Sort each category by total issues (descending - most issues first)
	sort.Slice(healthyTargets, func(i, j int) bool {
		return healthyTargets[i].TotalIssues > healthyTargets[j].TotalIssues
	})
	sort.Slice(issueTargets, func(i, j int) bool {
		return issueTargets[i].TotalIssues > issueTargets[j].TotalIssues
	})
	sort.Slice(criticalTargets, func(i, j int) bool {
		return criticalTargets[i].TotalIssues > criticalTargets[j].TotalIssues
	})

	avgUptime := 0.0
	if targetCount > 0 {
		avgUptime = totalUptime / float64(targetCount)
	}

	// Build the report
	var body strings.Builder
	body.WriteString(fmt.Sprintf("📊 Ping Monitor %s Summary Report\n", strings.Title(schedule)))
	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString(fmt.Sprintf("Report Period: %s (%s - %s)\n", 
		formatDuration(reportDuration),
		reportStart.Format("Jan 2 15:04"),
		now.Format("Jan 2 15:04")))
	body.WriteString(fmt.Sprintf("Total Targets Monitored: %d\n\n", targetCount))
	
	// Overall health section
	body.WriteString("📈 OVERALL HEALTH\n")
	
	// Healthy targets summary
	body.WriteString(fmt.Sprintf("  • All Up: %d targets", len(healthyTargets)))
	if len(healthyTargets) > 0 {
		// Show top 3 with most incidents from healthy category
		shown := 0
		hasIncidents := false
		for _, report := range healthyTargets {
			if report.TotalIssues > 0 && shown < 3 {
				if !hasIncidents {
					body.WriteString(":\n")
					hasIncidents = true
				}
				
				// Build incident breakdown
				incidentParts := []string{}
				if report.Stats.FailedChecks > 0 {
					incidentParts = append(incidentParts, fmt.Sprintf("%d down", report.Stats.FailedChecks))
				}
				if report.Stats.HighLatencyCount > 0 {
					incidentParts = append(incidentParts, fmt.Sprintf("%d high latency", report.Stats.HighLatencyCount))
				}
				if report.Stats.PacketLossEvents > 0 {
					incidentParts = append(incidentParts, fmt.Sprintf("%d packet loss", report.Stats.PacketLossEvents))
				}
				
				incidentBreakdown := ""
				if len(incidentParts) > 0 {
					incidentBreakdown = " (" + strings.Join(incidentParts, ", ") + ")"
				}
				
				body.WriteString(fmt.Sprintf("      - %s: %d incidents%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
				shown++
			}
			if shown == 3 {
				break
			}
		}
		if !hasIncidents {
			body.WriteString(" (all perfect)\n")
		}
	} else {
		body.WriteString("\n")
	}
	
	// Targets with issues summary
	body.WriteString(fmt.Sprintf("  • Issues: %d targets", len(issueTargets)))
	if len(issueTargets) > 0 {
		body.WriteString(":\n")
		for i, report := range issueTargets {
			// Build incident breakdown
			incidentParts := []string{}
			if report.Stats.FailedChecks > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d down", report.Stats.FailedChecks))
			}
			if report.Stats.HighLatencyCount > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d high latency", report.Stats.HighLatencyCount))
			}
			if report.Stats.PacketLossEvents > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d packet loss", report.Stats.PacketLossEvents))
			}
			
			incidentBreakdown := ""
			if len(incidentParts) > 0 {
				incidentBreakdown = " (" + strings.Join(incidentParts, ", ") + ")"
			}
			
			body.WriteString(fmt.Sprintf("      - %s: %d incidents%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
			if i == 2 {
				if len(issueTargets) > 3 {
					body.WriteString(fmt.Sprintf("      - (+%d more targets)\n", len(issueTargets)-3))
				}
				break
			}
		}
	} else {
		body.WriteString("\n")
	}
	
	// Critical targets summary
	body.WriteString(fmt.Sprintf("  • Critical: %d targets", len(criticalTargets)))
	if len(criticalTargets) > 0 {
		body.WriteString(":\n")
		for i, report := range criticalTargets {
			// Build incident breakdown
			incidentParts := []string{}
			if report.Stats.FailedChecks > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d down", report.Stats.FailedChecks))
			}
			if report.Stats.HighLatencyCount > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d high latency", report.Stats.HighLatencyCount))
			}
			if report.Stats.PacketLossEvents > 0 {
				incidentParts = append(incidentParts, fmt.Sprintf("%d packet loss", report.Stats.PacketLossEvents))
			}
			
			incidentBreakdown := ""
			if len(incidentParts) > 0 {
				incidentBreakdown = " (" + strings.Join(incidentParts, ", ") + ")"
			}
			
			body.WriteString(fmt.Sprintf("      - %s: %d incidents%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
			if i == 2 {
				if len(criticalTargets) > 3 {
					body.WriteString(fmt.Sprintf("      - (+%d more targets)\n", len(criticalTargets)-3))
				}
				break
			}
		}
	} else {
		body.WriteString("\n")
	}
	
	body.WriteString(fmt.Sprintf("  • Average Uptime: %.2f%%\n", avgUptime))
	if totalChecks > 0 {
		successRate := (float64(successfulChecks) / float64(totalChecks)) * 100
		body.WriteString(fmt.Sprintf("  • Total Checks: %s (%s successful)\n", 
			formatNumber(totalChecks), formatNumber(successfulChecks)))
		body.WriteString(fmt.Sprintf("  • Success Rate: %.2f%%\n", successRate))
	}
	body.WriteString("\n")

	// Healthy targets
	if len(healthyTargets) > 0 {
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		body.WriteString(fmt.Sprintf("🟢 HEALTHY TARGETS (99%%+ uptime) - %d\n", len(healthyTargets)))
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		for _, report := range healthyTargets {
			body.WriteString(fmt.Sprintf("%s (%s)\n", report.Target.Name, report.Target.TargetAddr))
			body.WriteString(fmt.Sprintf("  ✓ Uptime: %.2f%% (%s/%s checks)\n", 
				report.Uptime, formatNumber(report.Stats.SuccessfulChecks), formatNumber(report.Stats.TotalChecks)))
			if report.Stats.SuccessfulChecks > 0 {
				body.WriteString(fmt.Sprintf("  ⚡ Latency: %.2fms avg (%.2f-%.2fms)\n", 
					report.AvgLatency, report.MinLatency, report.MaxLatency))
			}
			body.WriteString(fmt.Sprintf("  📶 Packet Loss: %.1f%% avg (max: %d%%)\n", report.AvgPacketLoss, report.Stats.MaxPacketLoss))
			body.WriteString(fmt.Sprintf("  ⚠️  Total Incidents: %d (%d high latency, %d packet loss, %d failed)\n", 
				report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
			
			// Show recent events if any
			if len(report.Stats.RecentEvents) > 0 {
				body.WriteString("  📋 Recent Events:\n")
				for _, event := range report.Stats.RecentEvents {
					body.WriteString(fmt.Sprintf("    • [%s] %s\n", 
						event.Timestamp.Format("Jan 2 15:04:05"), formatEvent(event)))
				}
			}
			body.WriteString("\n")
		}
	}

	// Targets with issues
	if len(issueTargets) > 0 {
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		body.WriteString(fmt.Sprintf("🟡 TARGETS WITH ISSUES (95-99%% uptime) - %d\n", len(issueTargets)))
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		for _, report := range issueTargets {
			body.WriteString(fmt.Sprintf("%s (%s)\n", report.Target.Name, report.Target.TargetAddr))
			body.WriteString(fmt.Sprintf("  ⚠️  Uptime: %.2f%% (%s/%s checks)\n", 
				report.Uptime, formatNumber(report.Stats.SuccessfulChecks), formatNumber(report.Stats.TotalChecks)))
			body.WriteString(fmt.Sprintf("  ❌ Failed Checks: %s\n", formatNumber(report.Stats.FailedChecks)))
			if report.Stats.SuccessfulChecks > 0 {
				body.WriteString(fmt.Sprintf("  ⚡ Latency: %.2fms avg (%.2f-%.2fms)\n", 
					report.AvgLatency, report.MinLatency, report.MaxLatency))
			}
			body.WriteString(fmt.Sprintf("  📶 Packet Loss: %.1f%% avg (max: %d%%)\n", report.AvgPacketLoss, report.Stats.MaxPacketLoss))
			body.WriteString(fmt.Sprintf("  ⚠️  Total Incidents: %d (%d high latency, %d packet loss, %d failed)\n", 
				report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
			
			// Show recent events if any
			if len(report.Stats.RecentEvents) > 0 {
				body.WriteString("  📋 Recent Events:\n")
				for _, event := range report.Stats.RecentEvents {
					body.WriteString(fmt.Sprintf("    • [%s] %s\n", 
						event.Timestamp.Format("Jan 2 15:04:05"), formatEvent(event)))
				}
			}
			body.WriteString("\n")
		}
	}

	// Critical targets
	if len(criticalTargets) > 0 {
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		body.WriteString(fmt.Sprintf("🔴 CRITICAL TARGETS (<95%% uptime) - %d\n", len(criticalTargets)))
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		for _, report := range criticalTargets {
			body.WriteString(fmt.Sprintf("%s (%s)\n", report.Target.Name, report.Target.TargetAddr))
			body.WriteString(fmt.Sprintf("  🚨 Uptime: %.2f%% (%s/%s checks)\n", 
				report.Uptime, formatNumber(report.Stats.SuccessfulChecks), formatNumber(report.Stats.TotalChecks)))
			body.WriteString(fmt.Sprintf("  ❌ Failed Checks: %s\n", formatNumber(report.Stats.FailedChecks)))
			if report.Stats.SuccessfulChecks > 0 {
				body.WriteString(fmt.Sprintf("  ⚡ Latency: %.2fms avg (%.2f-%.2fms)\n", 
					report.AvgLatency, report.MinLatency, report.MaxLatency))
			}
			body.WriteString(fmt.Sprintf("  📶 Packet Loss: %.1f%% avg (max: %d%%)\n", report.AvgPacketLoss, report.Stats.MaxPacketLoss))
			body.WriteString(fmt.Sprintf("  ⚠️  Total Incidents: %d (%d high latency, %d packet loss, %d failed)\n", 
				report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
			
			// Show recent events if any
			if len(report.Stats.RecentEvents) > 0 {
				body.WriteString("  📋 Recent Events:\n")
				for _, event := range report.Stats.RecentEvents {
					body.WriteString(fmt.Sprintf("    • [%s] %s\n", 
						event.Timestamp.Format("Jan 2 15:04:05"), formatEvent(event)))
				}
			}
			body.WriteString("\n")
		}
	}

	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString(fmt.Sprintf("Next %s report: %s\n", schedule, pm.getNextReportTime().Format("Jan 2, 2006 15:04")))
	
	// Release the lock BEFORE sending email to prevent deadlock
	pm.mu.RUnlock()

	// Create email
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
		Subject:     subject,
		HtmlContent: fmt.Sprintf("<pre>%s</pre>", body.String()),
		TextContent: body.String(),
	}

	ctx := context.Background()
	_, _, err := pm.brevoClient.TransactionalEmailsApi.SendTransacEmail(ctx, email)
	if err != nil {
		log.Printf("❌ Failed to send summary report: %v", err)
		return fmt.Errorf("failed to send summary report: %v", err)
	}

	log.Printf("📊 Summary report sent successfully")
	
	// Reset stats after sending report - acquire lock again
	pm.mu.Lock()
	pm.statsStartTime = time.Now()
	for addr := range pm.targetStats {
		pm.targetStats[addr] = &TargetStats{
			MinLatency:   -1,
			RecentEvents: make([]EventRecord, 0),
		}
	}
	pm.mu.Unlock()

	return nil
}

// getNextReportTime calculates the next report time
func (pm *PingMonitor) getNextReportTime() time.Time {
	now := time.Now()
	
	// Parse report time
	reportTime := "00:00"
	if pm.config.SummaryReportTime != "" {
		reportTime = pm.config.SummaryReportTime
	}
	
	parts := strings.Split(reportTime, ":")
	hour, minute := 0, 0
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &hour)
		fmt.Sscanf(parts[1], "%d", &minute)
	}

	nextReport := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	
	if pm.config.SummaryReportSchedule == "weekly" {
		// Next Monday at report time
		daysUntilMonday := (8 - int(now.Weekday())) % 7
		if daysUntilMonday == 0 && now.After(nextReport) {
			daysUntilMonday = 7
		}
		nextReport = nextReport.AddDate(0, 0, daysUntilMonday)
	} else {
		// Daily - if time has passed today, schedule for tomorrow
		if now.After(nextReport) {
			nextReport = nextReport.AddDate(0, 0, 1)
		}
	}

	return nextReport
}

// startSummaryReportScheduler starts the summary report scheduler
func (pm *PingMonitor) startSummaryReportScheduler() {
	if !pm.config.SummaryReportEnabled {
		return
	}

	go func() {
		for {
			nextReport := pm.getNextReportTime()
			duration := time.Until(nextReport)
			
			log.Printf("📅 Next summary report scheduled for: %s (in %s)", 
				nextReport.Format("2006-01-02 15:04:05"), formatDuration(duration))
			
			time.Sleep(duration)
			
			log.Printf("📊 Generating %s summary report...", pm.config.SummaryReportSchedule)
			if err := pm.sendSummaryReport(); err != nil {
				log.Printf("❌ Failed to send summary report: %v", err)
			}
		}
	}()
}

// getTargetThreshold returns the effective threshold for a target
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

// monitorTarget monitors a single target with graceful degradation
func (pm *PingMonitor) monitorTarget(target Target) {
	// Acquire semaphore to limit concurrent pings
	pm.semaphore <- struct{}{}
	defer func() { 
		<-pm.semaphore
		// Recover from any panics to ensure graceful degradation
		if r := recover(); r != nil {
			log.Printf("🆘 Recovered from panic in monitorTarget for %s: %v (continuing monitoring)", 
				formatTargetInfo(target), r)
		}
	}()

	success, packetLoss, rttMs := pm.pingTarget(target)
	
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	// Check if status changed (down/up)
	wasDown := pm.downTargets[target.TargetAddr]
	
	if !success && !wasDown {
		// Target just went down
		pm.downTargets[target.TargetAddr] = true
		pm.downSince[target.TargetAddr] = time.Now()
		log.Printf("🔴 ALERT: %s is now DOWN", formatTargetInfo(target))
		pm.mu.Unlock()
		
		pm.recordEvent(target, "down", 0, 0, 0)
		
		if pm.canSendAlert(target, "down") {
			if err := pm.sendEmail(target, "down", 0, packetLoss, 0); err != nil {
				log.Printf("⚠️  Failed to send down notification for %s: %v (continuing monitoring)", target.Name, err)
			} else {
				pm.recordAlert(target, "down")
			}
		}
		pm.mu.Lock()
	} else if success && wasDown {
		// Target came back up
		downtime := time.Since(pm.downSince[target.TargetAddr])
		delete(pm.downTargets, target.TargetAddr)
		delete(pm.downSince, target.TargetAddr)
		log.Printf("🟢 RECOVERY: %s is now UP (was down for %s)", formatTargetInfo(target), formatDuration(downtime))
		pm.mu.Unlock()
		
		pm.recordEvent(target, "up", rttMs, 0, downtime)
		
		if pm.canSendAlert(target, "up") {
			if err := pm.sendEmail(target, "up", rttMs, packetLoss, downtime); err != nil {
				log.Printf("⚠️  Failed to send recovery notification for %s: %v (continuing monitoring)", target.Name, err)
			} else {
				pm.recordAlert(target, "up")
			}
		}
		pm.mu.Lock()
	}
	
	// Check packet loss threshold (only if target is up)
	if success {
		packetLossThreshold := pm.getPacketLossThreshold(target)
		hadPacketLoss := pm.packetLossTargets[target.TargetAddr]
		hasPacketLoss := packetLoss >= packetLossThreshold
		
		if hasPacketLoss && !hadPacketLoss {
			pm.packetLossTargets[target.TargetAddr] = true
			pm.packetLossSince[target.TargetAddr] = time.Now()
			log.Printf("🟠 ALERT: %s has PACKET LOSS (%d%% >= %d%%)", 
				formatTargetInfo(target), packetLoss, packetLossThreshold)
			pm.mu.Unlock()
			
			pm.recordEvent(target, "packet_loss", float64(packetLoss), float64(packetLossThreshold), 0)
			
			if pm.canSendAlert(target, "packet_loss") {
				if err := pm.sendEmail(target, "packet_loss", rttMs, packetLoss, 0); err != nil {
					log.Printf("⚠️  Failed to send packet loss notification for %s: %v (continuing monitoring)", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss")
				}
			}
			pm.mu.Lock()
		} else if !hasPacketLoss && hadPacketLoss {
			duration := time.Duration(0)
			if startTime, exists := pm.packetLossSince[target.TargetAddr]; exists {
				duration = time.Since(startTime)
			}
			delete(pm.packetLossTargets, target.TargetAddr)
			delete(pm.packetLossSince, target.TargetAddr)
			log.Printf("🟢 RECOVERY: %s packet loss is now NORMAL (%d%% < %d%%)", 
				formatTargetInfo(target), packetLoss, packetLossThreshold)
			pm.mu.Unlock()
			
			pm.recordEvent(target, "packet_loss_normal", float64(packetLoss), float64(packetLossThreshold), duration)
			
			if pm.canSendAlert(target, "packet_loss_normal") {
				if err := pm.sendEmail(target, "packet_loss_normal", rttMs, packetLoss, duration); err != nil {
					log.Printf("⚠️  Failed to send packet loss recovery notification for %s: %v (continuing monitoring)", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss_normal")
				}
			}
			pm.mu.Lock()
		}
		
		// Check latency threshold
		threshold := pm.getTargetThreshold(target)
		wasSlow := pm.slowTargets[target.TargetAddr]
		isSlow := rttMs > float64(threshold)
		
		if isSlow && !wasSlow {
			pm.slowTargets[target.TargetAddr] = true
			pm.slowSince[target.TargetAddr] = time.Now()
			log.Printf("🟡 ALERT: %s has HIGH LATENCY (%.2fms > %dms)", 
				formatTargetInfo(target), rttMs, threshold)
			pm.mu.Unlock()
			
			pm.recordEvent(target, "high_latency", rttMs, float64(threshold), 0)
			
			if pm.canSendAlert(target, "slow") {
				if err := pm.sendEmail(target, "slow", rttMs, packetLoss, 0); err != nil {
					log.Printf("⚠️  Failed to send high latency notification for %s: %v (continuing monitoring)", target.Name, err)
				} else {
					pm.recordAlert(target, "slow")
				}
			}
			pm.mu.Lock()
		} else if !isSlow && wasSlow {
			duration := time.Duration(0)
			if startTime, exists := pm.slowSince[target.TargetAddr]; exists {
				duration = time.Since(startTime)
			}
			delete(pm.slowTargets, target.TargetAddr)
			delete(pm.slowSince, target.TargetAddr)
			log.Printf("🟢 RECOVERY: %s latency is now NORMAL (%.2fms <= %dms)", 
				formatTargetInfo(target), rttMs, threshold)
			pm.mu.Unlock()
			
			pm.recordEvent(target, "latency_normal", rttMs, float64(threshold), duration)
			
			if pm.canSendAlert(target, "normal") {
				if err := pm.sendEmail(target, "normal", rttMs, packetLoss, duration); err != nil {
					log.Printf("⚠️  Failed to send latency recovery notification for %s: %v (continuing monitoring)", target.Name, err)
				} else {
					pm.recordAlert(target, "normal")
				}
			}
			pm.mu.Lock()
		}
	}
}

// Start begins the monitoring process
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
	log.Printf("   • Default Timeout: %d seconds", pm.config.DefaultTimeoutSeconds)
	log.Printf("   • Packet Loss Threshold: %d%%", pm.config.PacketLossThresholdPercent)
	log.Printf("   • Alert Cooldown: %d minutes", pm.config.AlertCooldownMinutes)
	log.Printf("   • Email Rate Limit: %d/hour", pm.config.EmailRateLimitPerHour)
	log.Printf("   • Max Concurrent Pings: %d", pm.config.MaxConcurrentPings)
	
	if pm.config.SummaryReportEnabled {
		log.Printf("   • Summary Reports: %s at %s", 
			strings.Title(pm.config.SummaryReportSchedule), 
			pm.config.SummaryReportTime)
		pm.startSummaryReportScheduler()
	}

	// Shuffle targets to randomize order
	targets := make([]Target, len(pm.config.Targets))
	copy(targets, pm.config.Targets)
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(targets), func(i, j int) {
		targets[i], targets[j] = targets[j], targets[i]
	})
	log.Printf("🔀 Targets shuffled for randomized monitoring order")

	// Calculate delay between checks
	intervalSeconds := time.Duration(pm.config.PingIntervalSeconds) * time.Second
	delayBetweenTargets := intervalSeconds / time.Duration(numTargets)

	log.Printf("📊 Distributing pings with %v delay between targets for continuous monitoring", 
		delayBetweenTargets)

	// Start monitoring goroutines with graceful degradation
	for i, target := range targets {
		initialDelay := time.Duration(i) * delayBetweenTargets
		
		go func(t Target, delay time.Duration) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🆘 Monitoring goroutine for %s panicked: %v (restarting)", 
						formatTargetInfo(t), r)
					// Restart the goroutine
					go func(tgt Target, dly time.Duration) {
						time.Sleep(dly)
						pm.monitorTarget(tgt)
						ticker := time.NewTicker(intervalSeconds)
						defer ticker.Stop()
						for range ticker.C {
							pm.monitorTarget(tgt)
						}
					}(t, delay)
				}
			}()

			time.Sleep(delay)
			pm.monitorTarget(t)
			
			ticker := time.NewTicker(intervalSeconds)
			defer ticker.Stop()
			
			for range ticker.C {
				pm.monitorTarget(t)
			}
		}(target, initialDelay)
	}

	log.Printf("✅ All monitoring goroutines started with graceful degradation")

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
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}

	// Validate configuration
	if err := ValidateConfig(config); err != nil {
		log.Fatalf("❌ %v", err)
	}
	
	log.Printf("✅ Configuration validated successfully")
	
	// Set default ping count
	if config.PingCount <= 0 {
		config.PingCount = 3
		log.Printf("ℹ️  Ping count not specified, using default: %d", config.PingCount)
	}

	// Create monitor
	monitor := NewPingMonitor(config)

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		log.Println("⏹️  Shutting down ping monitor gracefully...")
		os.Exit(0)
	}()

	// Start monitoring
	monitor.Start()
}
