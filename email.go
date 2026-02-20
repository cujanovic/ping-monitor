package main

import (
	"context"
	"fmt"
	"log"
	"time"

	brevo "github.com/getbrevo/brevo-go/lib"
)

// getAlertPriority returns the priority level of an alert type (higher = more critical)
func getAlertPriority(alertType string) int {
	switch alertType {
	case "down":
		return 5 // Highest priority - critical
	case "up":
		return 4 // High priority - recovery from critical
	case "packet_loss":
		return 3 // Medium-high priority
	case "packet_loss_normal":
		return 2 // Medium priority - recovery
	case "slow":
		return 2 // Medium priority
	case "normal":
		return 1 // Low priority - recovery
	default:
		return 1
	}
}

// isCriticalAlert returns true if the alert is critical (down/up)
func isCriticalAlert(alertType string) bool {
	return alertType == "down" || alertType == "up"
}

// cleanupOldTimestamps removes timestamps older than 1 hour from a slice
func cleanupOldTimestamps(timestamps []time.Time) []time.Time {
	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)
	valid := make([]time.Time, 0)
	for _, t := range timestamps {
		if t.After(oneHourAgo) {
			valid = append(valid, t)
		}
	}
	return valid
}

// areAllTargetsDown checks if all enabled targets are currently down
// This is used as a false positive check: if all targets are down simultaneously,
// it likely indicates a VPN/monitoring infrastructure issue rather than actual target failures
func (pm *PingMonitor) areAllTargetsDown() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	enabledCount := 0
	downCount := 0
	
	// Count enabled targets and how many are down
	for _, target := range pm.config.Targets {
		// Skip disabled targets
		if pm.disabledTargets[target.TargetAddr] {
			continue
		}
		
		enabledCount++
		if pm.downTargets[target.TargetAddr] {
			downCount++
		}
	}
	
	// If all enabled targets are down (and there's at least one enabled target), return true
	return enabledCount > 0 && downCount == enabledCount
}

// canSendAlert checks if an alert can be sent based on cooldown and rate limiting
// Implements priority-based, per-target, and per-alert-type rate limiting
// Also includes false positive check: suppresses alerts when all targets are down (VPN issue)
func (pm *PingMonitor) canSendAlert(target Target, alertType string) bool {
	// False positive check: if all enabled targets are down, suppress alerts
	// This indicates a VPN/monitoring infrastructure issue rather than actual target failures
	if pm.areAllTargetsDown() {
		pm.mu.Lock()
		if !pm.allDownSuppressionActive {
			pm.allDownSuppressionActive = true
			pm.mu.Unlock()
			log.Printf("🚫 All enabled targets are down - suppressing alerts (likely VPN/monitoring infrastructure issue)")
		} else {
			pm.mu.Unlock()
		}
		return false
	}
	pm.mu.Lock()
	pm.allDownSuppressionActive = false
	pm.mu.Unlock()

	pm.mu.RLock()
	key := AlertKey{TargetAddr: target.TargetAddr, AlertType: alertType}
	lastAlert, exists := pm.lastAlertTime[key]
	pm.mu.RUnlock()

	// Check cooldown
	if exists {
		cooldownDuration := time.Duration(pm.config.AlertCooldownMinutes) * time.Minute
		if time.Since(lastAlert) < cooldownDuration {
			log.Printf("⏱️  Alert cooldown active for %s (%s) - %v remaining", 
				formatTargetInfo(target), alertType, cooldownDuration-time.Since(lastAlert))
			return false
		}
	}

	pm.emailMu.Lock()
	defer pm.emailMu.Unlock()
	
	// Cleanup old timestamps (rolling window)
	pm.emailsSentThisHour = cleanupOldTimestamps(pm.emailsSentThisHour)
	
	// Cleanup per-target timestamps
	if pm.emailsSentPerTarget[target.TargetAddr] != nil {
		cleaned := cleanupOldTimestamps(pm.emailsSentPerTarget[target.TargetAddr])
		if len(cleaned) == 0 {
			delete(pm.emailsSentPerTarget, target.TargetAddr) // Remove empty entries
		} else {
			pm.emailsSentPerTarget[target.TargetAddr] = cleaned
		}
	}
	
	// Cleanup per-alert-type timestamps
	if pm.emailsSentPerAlertType[alertType] != nil {
		cleaned := cleanupOldTimestamps(pm.emailsSentPerAlertType[alertType])
		if len(cleaned) == 0 {
			delete(pm.emailsSentPerAlertType, alertType) // Remove empty entries
		} else {
			pm.emailsSentPerAlertType[alertType] = cleaned
		}
	}
	
	// Cleanup all other expired entries to prevent memory leaks
	for targetAddr, timestamps := range pm.emailsSentPerTarget {
		if targetAddr != target.TargetAddr { // Skip the one we already cleaned
			cleaned := cleanupOldTimestamps(timestamps)
			if len(cleaned) == 0 {
				delete(pm.emailsSentPerTarget, targetAddr)
			} else {
				pm.emailsSentPerTarget[targetAddr] = cleaned
			}
		}
	}
	
	for alertTypeKey, timestamps := range pm.emailsSentPerAlertType {
		if alertTypeKey != alertType { // Skip the one we already cleaned
			cleaned := cleanupOldTimestamps(timestamps)
			if len(cleaned) == 0 {
				delete(pm.emailsSentPerAlertType, alertTypeKey)
			} else {
				pm.emailsSentPerAlertType[alertTypeKey] = cleaned
			}
		}
	}

	// Calculate critical reserve
	criticalReserve := (pm.config.EmailRateLimitPerHour * pm.config.EmailCriticalReservePercent) / 100
	nonCriticalLimit := pm.config.EmailRateLimitPerHour - criticalReserve
	
	// Check per-alert-type limit
	if limit, exists := pm.config.EmailPerAlertTypeLimits[alertType]; exists && limit > 0 {
		alertTypeSlice := pm.emailsSentPerAlertType[alertType]
		if alertTypeSlice == nil {
			alertTypeSlice = []time.Time{} // Initialize if nil
		}
		alertTypeCount := len(alertTypeSlice)
		if alertTypeCount >= limit {
			log.Printf("⚠️  Per-alert-type rate limit reached for %s (%d/%d per hour)", 
				alertType, alertTypeCount, limit)
			return false
		}
	}

	// Check per-target limit
	if pm.config.EmailRateLimitPerTargetPerHour > 0 {
		targetSlice := pm.emailsSentPerTarget[target.TargetAddr]
		if targetSlice == nil {
			targetSlice = []time.Time{} // Initialize if nil
		}
		targetCount := len(targetSlice)
		if targetCount >= pm.config.EmailRateLimitPerTargetPerHour {
			log.Printf("⚠️  Per-target rate limit reached for %s (%d/%d per hour)", 
				formatTargetInfo(target), targetCount, pm.config.EmailRateLimitPerTargetPerHour)
			return false
		}
	}

	// Check global rate limit with priority handling
	isCritical := isCriticalAlert(alertType)
	globalCount := len(pm.emailsSentThisHour)
	
	// Count non-critical emails sent in the last hour
	// We count by summing non-critical alert types from per-alert-type map
	// This works because recordAlert() always records in per-alert-type map
	nonCriticalCount := 0
	if !isCritical {
		// Count non-critical emails by checking per-alert-type counters
		for alertTypeKey, timestamps := range pm.emailsSentPerAlertType {
			if !isCriticalAlert(alertTypeKey) && timestamps != nil {
				// Count only timestamps within the last hour (already cleaned up above)
				nonCriticalCount += len(timestamps)
			}
		}
	}
	
	if isCritical {
		// Critical alerts can use the full limit (including reserve)
		if globalCount >= pm.config.EmailRateLimitPerHour {
			log.Printf("⚠️  Global email rate limit reached (%d/hour) - critical alert blocked", 
				pm.config.EmailRateLimitPerHour)
			return false
		}
	} else {
		// Non-critical alerts are limited to non-critical quota
		if nonCriticalCount >= nonCriticalLimit {
			log.Printf("⚠️  Non-critical email rate limit reached (%d/%d per hour) - %s alert blocked", 
				nonCriticalCount, nonCriticalLimit, alertType)
			return false
		}
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
	// Record in global counter
	pm.emailsSentThisHour = append(pm.emailsSentThisHour, now)
	
	// Record per-target
	if pm.emailsSentPerTarget[target.TargetAddr] == nil {
		pm.emailsSentPerTarget[target.TargetAddr] = make([]time.Time, 0)
	}
	pm.emailsSentPerTarget[target.TargetAddr] = append(pm.emailsSentPerTarget[target.TargetAddr], now)
	
	// Record per-alert-type
	if pm.emailsSentPerAlertType[alertType] == nil {
		pm.emailsSentPerAlertType[alertType] = make([]time.Time, 0)
	}
	pm.emailsSentPerAlertType[alertType] = append(pm.emailsSentPerAlertType[alertType], now)
	
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
