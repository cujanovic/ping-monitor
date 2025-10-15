package main

import (
	"context"
	"fmt"
	"log"
	"time"

	brevo "github.com/getbrevo/brevo-go/lib"
)

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
			log.Printf("⏱️  Alert cooldown active for %s (%s)", formatTargetInfo(target), alertType)
			return false
		}
	}

	// Check rate limit
	pm.emailMu.Lock()
	defer pm.emailMu.Unlock()

	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)
	validEmails := make([]time.Time, 0)
	for _, t := range pm.emailsSentThisHour {
		if t.After(oneHourAgo) {
			validEmails = append(validEmails, t)
		}
	}
	pm.emailsSentThisHour = validEmails

	if len(pm.emailsSentThisHour) >= pm.config.EmailRateLimitPerHour {
		log.Printf("⚠️  Email rate limit reached (%d/hour)", pm.config.EmailRateLimitPerHour)
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
