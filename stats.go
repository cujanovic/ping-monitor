package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	brevo "github.com/getbrevo/brevo-go/lib"
)

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
		
		if stats.MinLatency < 0 || latencyMs < stats.MinLatency {
			stats.MinLatency = latencyMs
		}
		if latencyMs > stats.MaxLatency {
			stats.MaxLatency = latencyMs
		}
		
		threshold := pm.getTargetThreshold(target)
		if latencyMs > float64(threshold) {
			stats.HighLatencyCount++
		}
	} else {
		stats.FailedChecks++
	}

	stats.TotalPacketLoss += int64(packetLoss)
	
	if packetLoss > stats.MaxPacketLoss {
		stats.MaxPacketLoss = packetLoss
	}
	
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
	
	// Keep last N events based on config (default: 500 for 24h+ of incidents)
	maxEvents := pm.config.RecentEventsBufferSize
	if len(stats.RecentEvents) > maxEvents {
		stats.RecentEvents = stats.RecentEvents[len(stats.RecentEvents)-maxEvents:]
	}

	// Mark state as dirty so it gets saved to disk
	// This triggers event-driven state persistence
	pm.stateSavePending = true
}

// addLog adds a log entry to the pending buffer
func (pm *PingMonitor) addLog(message string) {
	pm.asyncLogger.Log(message)
}

// Removed logBufferFlusher and flushLogBuffer - now handled by AsyncLogger

// getRecentLogs returns the recent log entries
func (pm *PingMonitor) getRecentLogs() []LogEntry {
	return pm.asyncLogger.GetLogs()
}

// saveReportToFile saves a report to disk
func (pm *PingMonitor) saveReportToFile(reportText string) error {
	if pm.config.ReportsDirectory == "" {
		return nil
	}

	timestamp := pm.getReportTime().Format("2006-01-02_15-04-05")
	filename := fmt.Sprintf("report_%s.txt", timestamp)
	filepath := fmt.Sprintf("%s/%s", pm.config.ReportsDirectory, filename)

	if err := os.WriteFile(filepath, []byte(reportText), 0644); err != nil {
		return fmt.Errorf("failed to write report file: %v", err)
	}

	log.Printf("💾 Report saved to: %s", filepath)
	pm.addLog(fmt.Sprintf("Report saved to: %s", filename))

	pm.cleanupOldReportsIfNeeded()

	return nil
}

// cleanupOldReportsIfNeeded removes old report files
func (pm *PingMonitor) cleanupOldReportsIfNeeded() {
	if pm.config.ReportsDirectory == "" {
		return
	}

	entries, err := os.ReadDir(pm.config.ReportsDirectory)
	if err != nil {
		log.Printf("⚠️  Failed to read reports directory: %v", err)
		return
	}

	var reportFiles []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "report_") && strings.HasSuffix(entry.Name(), ".txt") {
			reportFiles = append(reportFiles, entry)
		}
	}

	if len(reportFiles) <= pm.config.ReportsKeepCount {
		return
	}

	type fileWithInfo struct {
		entry os.DirEntry
		info  os.FileInfo
	}
	
	filesWithInfo := make([]fileWithInfo, 0, len(reportFiles))
	for _, entry := range reportFiles {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		filesWithInfo = append(filesWithInfo, fileWithInfo{entry, info})
	}

	sort.Slice(filesWithInfo, func(i, j int) bool {
		return filesWithInfo[i].info.ModTime().After(filesWithInfo[j].info.ModTime())
	})

	for i := pm.config.ReportsKeepCount; i < len(filesWithInfo); i++ {
		filepath := fmt.Sprintf("%s/%s", pm.config.ReportsDirectory, filesWithInfo[i].entry.Name())
		if err := os.Remove(filepath); err != nil {
			log.Printf("⚠️  Failed to remove old report %s: %v", filesWithInfo[i].entry.Name(), err)
		} else {
			log.Printf("🗑️  Removed old report: %s", filesWithInfo[i].entry.Name())
		}
	}
}

// getAllReports returns a list of all available reports
func (pm *PingMonitor) getAllReports() []string {
	if pm.config.ReportsDirectory == "" {
		return nil
	}

	entries, err := os.ReadDir(pm.config.ReportsDirectory)
	if err != nil {
		return nil
	}

	var reports []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "report_") && strings.HasSuffix(entry.Name(), ".txt") {
			reports = append(reports, entry.Name())
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(reports)))
	
	return reports
}

// loadLatestReport loads the most recent report from disk
func (pm *PingMonitor) loadLatestReport() {
	if pm.config.ReportsDirectory == "" {
		return
	}

	entries, err := os.ReadDir(pm.config.ReportsDirectory)
	if err != nil {
		log.Printf("⚠️  Failed to read reports directory: %v", err)
		return
	}

	var latestEntry os.DirEntry
	var latestModTime time.Time
	
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "report_") && strings.HasSuffix(entry.Name(), ".txt") {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if latestEntry == nil || info.ModTime().After(latestModTime) {
				latestEntry = entry
				latestModTime = info.ModTime()
			}
		}
	}

	if latestEntry == nil {
		log.Printf("ℹ️  No previous reports found")
		pm.addLog("ℹ️  No previous reports found")
		return
	}

	filepath := fmt.Sprintf("%s/%s", pm.config.ReportsDirectory, latestEntry.Name())
	data, err := os.ReadFile(filepath)
	if err != nil {
		log.Printf("⚠️  Failed to load report %s: %v", latestEntry.Name(), err)
		return
	}

	pm.lastEmailReportMu.Lock()
	pm.lastEmailReport = string(data)
	pm.lastEmailReportMu.Unlock()

	msg := fmt.Sprintf("📂 Loaded previous report: %s", latestEntry.Name())
	log.Printf(msg)
	pm.addLog(msg)
}

// sendSummaryReport sends a summary report email
func (pm *PingMonitor) sendSummaryReport() error {
	pm.mu.RLock()
	reportDuration := time.Since(pm.statsStartTime)
	schedule := pm.config.SummaryReportSchedule
	now := pm.getReportTime()  // Use getReportTime() to apply offset
	reportStart := now.Add(-reportDuration)
	
	subject := fmt.Sprintf("📊 Ping Monitor %s Summary Report", strings.Title(schedule))
	
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

	var body strings.Builder
	pm.buildReportBody(&body, schedule, reportDuration, reportStart, now, targetCount,
		healthyTargets, issueTargets, criticalTargets, avgUptime, totalChecks, successfulChecks)
	
	reportText := body.String()
	pm.lastEmailReportMu.Lock()
	pm.lastEmailReport = reportText
	pm.lastEmailReportMu.Unlock()
	
	if err := pm.saveReportToFile(reportText); err != nil {
		log.Printf("⚠️  Failed to save report to file: %v", err)
	}
	
	pm.mu.RUnlock()

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
		pm.addLog(fmt.Sprintf("Failed to send summary report: %v", err))
		return fmt.Errorf("failed to send summary report: %v", err)
	}

	log.Printf("📊 Summary report sent successfully")
	pm.addLog("Summary report sent successfully")
	
	// Note: We no longer reset stats here. Stats persist and are cleaned up based on the
	// configured recent_incidents_hours period. This allows stats to cover the full
	// configured time window (e.g., 24 hours) regardless of report frequency.
	
	return nil
}

// buildReportBody builds the summary report body (continued in next message due to length)
func (pm *PingMonitor) buildReportBody(body *strings.Builder, schedule string, reportDuration time.Duration,
	reportStart, now time.Time, targetCount int, healthyTargets, issueTargets, criticalTargets []TargetReport,
	avgUptime float64, totalChecks, successfulChecks int64) {
	
	body.WriteString(fmt.Sprintf("Report Period: %s (%s - %s)\n", 
		formatDuration(reportDuration),
		reportStart.Format("Jan 2 15:04"),
		now.Format("Jan 2 15:04")))
	body.WriteString(fmt.Sprintf("Total Targets Monitored: %d\n\n", targetCount))

	// Add Recent Incidents Summary FIRST
	incidents := pm.getRecentIncidents()
	summary := pm.getIncidentsSummary(int(reportDuration.Hours()))
	if summary["TotalIncidents"].(int) > 0 {
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		body.WriteString(fmt.Sprintf("🚨 INCIDENT EVENTS (Last %d hours)\n\n", int(reportDuration.Hours())))
		body.WriteString(fmt.Sprintf("Total Alert Events: %d | ", summary["TotalIncidents"].(int)))
		body.WriteString(fmt.Sprintf("Resolved: %d (%d%%) | ", 
			summary["ResolvedCount"].(int), summary["ResolvedPercent"].(int)))
		body.WriteString(fmt.Sprintf("Avg Resolution: %s\n", summary["AvgResolution"].(string)))
		body.WriteString(fmt.Sprintf("  • Down Events: %d (avg resolution: %s)\n", 
			summary["DowntimeCount"].(int), summary["AvgDowntimeResolution"].(string)))
		body.WriteString(fmt.Sprintf("  • High Latency Events: %d (avg resolution: %s)\n", 
			summary["HighLatencyCount"].(int), summary["AvgHighLatencyResolution"].(string)))
		body.WriteString(fmt.Sprintf("  • Packet Loss Events: %d (avg resolution: %s)\n", 
			summary["PacketLossCount"].(int), summary["AvgPacketLossResolution"].(string)))
		
		if summary["FirstIncident"].(string) != "" {
			body.WriteString(fmt.Sprintf("First Event: %s\n", summary["FirstIncident"].(string)))
			if summary["LastResolved"].(string) != "" {
				body.WriteString(fmt.Sprintf("Last Resolved: %s\n", summary["LastResolved"].(string)))
			}
			if summary["TotalDuration"].(string) != "" {
				body.WriteString(fmt.Sprintf("Total Downtime: %s\n", summary["TotalDuration"].(string)))
			}
		}
		body.WriteString("\n")
		
		// Show top 5 most recent incidents
		body.WriteString("Recent Alert Events:\n")
		maxIncidents := 5
		if len(incidents) < maxIncidents {
			maxIncidents = len(incidents)
		}
		for i := 0; i < maxIncidents; i++ {
			incident := incidents[i]
			resolvedStr := ""
			if incident.IsResolved {
				if incident.Duration != "" {
					resolvedStr = fmt.Sprintf(" [Resolved in %s]", incident.Duration)
				} else {
					resolvedStr = " [Resolved]"
				}
			} else {
				resolvedStr = " [ONGOING]"
			}
			body.WriteString(fmt.Sprintf("  • [%s] %s - %s%s\n", 
				incident.Timestamp, incident.TargetName, incident.Description, resolvedStr))
		}
		if len(incidents) > maxIncidents {
			body.WriteString(fmt.Sprintf("  ... and %d more events\n", len(incidents)-maxIncidents))
		}
		body.WriteString("\n")
	}
	
	// Overall Health SECOND
	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString("📈 OVERALL HEALTH\n\n")
	pm.writeTargetSummary(body, "All Up", healthyTargets, 3, true)
	pm.writeTargetSummary(body, "Issues", issueTargets, 3, false)
	pm.writeTargetSummary(body, "Critical", criticalTargets, 3, false)
	
	body.WriteString(fmt.Sprintf("  • Average Uptime: %.2f%%\n", avgUptime))
	if totalChecks > 0 {
		successRate := (float64(successfulChecks) / float64(totalChecks)) * 100
		body.WriteString(fmt.Sprintf("  • Total Checks: %s (%s successful)\n", 
			formatNumber(totalChecks), formatNumber(successfulChecks)))
		body.WriteString(fmt.Sprintf("  • Success Rate: %.2f%%\n", successRate))
	}
	body.WriteString("\n")

	// Detailed target information - only show targets with issues or recent events
	targetsWithDetails := []TargetReport{}
	for _, report := range append(append(healthyTargets, issueTargets...), criticalTargets...) {
		// Show target if it has failed checks OR recent events
		if report.TotalIssues > 0 || len(report.Stats.RecentEvents) > 0 {
			targetsWithDetails = append(targetsWithDetails, report)
		}
	}
	
	if len(targetsWithDetails) > 0 {
		body.WriteString(strings.Repeat("━", 60) + "\n\n")
		body.WriteString("📊 DETAILED TARGET INFORMATION\n\n")
		body.WriteString("Targets with failed checks or recent alert events:\n\n")
		pm.writeDetailedTargets(body, targetsWithDetails)
	}

	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	// Apply offset to next report time for display
	nextReportDisplay := pm.getNextReportTime().Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
	body.WriteString(fmt.Sprintf("Next %s report: %s\n", schedule, nextReportDisplay.Format("Jan 2, 2006 15:04")))
}

func (pm *PingMonitor) writeTargetSummary(body *strings.Builder, label string, targets []TargetReport, maxShow int, allowPerfect bool) {
	body.WriteString(fmt.Sprintf("  • %s: %d targets", label, len(targets)))
	if len(targets) > 0 {
		shown := 0
		hasIncidents := false
		for _, report := range targets {
			if report.TotalIssues > 0 && shown < maxShow {
				if !hasIncidents {
					body.WriteString(":\n")
					hasIncidents = true
				}
				
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
				
				body.WriteString(fmt.Sprintf("      - %s: %d failed checks%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
				shown++
			}
			if shown == maxShow {
				break
			}
		}
		if !hasIncidents && allowPerfect {
			body.WriteString(" (all perfect)\n")
		} else if !hasIncidents {
			body.WriteString(":\n")
			for i, report := range targets {
				if i < maxShow {
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
					
					body.WriteString(fmt.Sprintf("      - %s: %d failed checks%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
				}
				if i == maxShow-1 && len(targets) > maxShow {
					body.WriteString(fmt.Sprintf("      - (+%d more targets)\n", len(targets)-maxShow))
					break
				}
			}
		}
	} else {
		body.WriteString("\n")
	}
}

func (pm *PingMonitor) writeTargetDetails(body *strings.Builder, title string, targets []TargetReport) {
	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString(fmt.Sprintf("%s - %d\n", title, len(targets)))
	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	for _, report := range targets {
		body.WriteString(fmt.Sprintf("%s (%s)\n", report.Target.Name, report.Target.TargetAddr))
		body.WriteString(fmt.Sprintf("  ✓ Uptime: %.2f%% (%s/%s checks)\n", 
			report.Uptime, formatNumber(report.Stats.SuccessfulChecks), formatNumber(report.Stats.TotalChecks)))
		if report.Stats.SuccessfulChecks > 0 {
			body.WriteString(fmt.Sprintf("  ⚡ Latency: %.2fms avg (%.2f-%.2fms)\n", 
				report.AvgLatency, report.MinLatency, report.MaxLatency))
		}
		body.WriteString(fmt.Sprintf("  📶 Packet Loss: %.1f%% avg (max: %d%%)\n", report.AvgPacketLoss, report.Stats.MaxPacketLoss))
		body.WriteString(fmt.Sprintf("  ⚠️  Failed Checks: %d total (%d high latency, %d packet loss, %d down)\n", 
			report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
		
		if len(report.Stats.RecentEvents) > 0 {
			body.WriteString("  📋 Recent Events:\n")
			for _, event := range report.Stats.RecentEvents {
				// Apply time offset to event timestamps
				eventTime := event.Timestamp.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
				body.WriteString(fmt.Sprintf("    • [%s] %s\n", 
					eventTime.Format("Jan 2 15:04:05"), formatEvent(event)))
			}
		}
		body.WriteString("\n")
	}
}

// writeDetailedTargets writes detailed information for targets (improved formatting)
func (pm *PingMonitor) writeDetailedTargets(body *strings.Builder, targets []TargetReport) {
	for _, report := range targets {
		body.WriteString(fmt.Sprintf("%s (%s)\n", report.Target.Name, report.Target.TargetAddr))
		body.WriteString(fmt.Sprintf("  ✓ Uptime: %.2f%% (%s/%s checks)\n", 
			report.Uptime, formatNumber(report.Stats.SuccessfulChecks), formatNumber(report.Stats.TotalChecks)))
		if report.Stats.SuccessfulChecks > 0 {
			body.WriteString(fmt.Sprintf("  ⚡ Latency: %.2fms avg (%.2f-%.2fms)\n", 
				report.AvgLatency, report.MinLatency, report.MaxLatency))
		}
		body.WriteString(fmt.Sprintf("  📶 Packet Loss: %.1f%% avg (max: %d%%)\n", report.AvgPacketLoss, report.Stats.MaxPacketLoss))
		
		// Only show failed checks line if there are any
		if report.TotalIssues > 0 {
			body.WriteString(fmt.Sprintf("  ⚠️  Failed Checks: %d total (%d high latency, %d packet loss, %d down)\n", 
				report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
		}
		
		// Show recent events (limited to last 20 to keep email size reasonable)
		if len(report.Stats.RecentEvents) > 0 {
			body.WriteString("  📋 Recent Events:\n")
			maxEvents := 20
			if len(report.Stats.RecentEvents) < maxEvents {
				maxEvents = len(report.Stats.RecentEvents)
			}
			for i := 0; i < maxEvents; i++ {
				event := report.Stats.RecentEvents[i]
				// Apply time offset to event timestamps
				eventTime := event.Timestamp.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
				body.WriteString(fmt.Sprintf("    • [%s] %s\n", 
					eventTime.Format("Jan 2 15:04:05"), formatEvent(event)))
			}
			if len(report.Stats.RecentEvents) > maxEvents {
				body.WriteString(fmt.Sprintf("    ... and %d more events\n", len(report.Stats.RecentEvents)-maxEvents))
			}
		}
		body.WriteString("\n")
	}
}

// getNextReportTime calculates the next report time (without offset, for scheduling)
func (pm *PingMonitor) getNextReportTime() time.Time {
	now := time.Now()
	
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

	// Apply offset: user specifies time in THEIR timezone, we convert to server time
	// For example: user wants 07:00 local time with offset +1
	// Server (UTC) should trigger at 06:00 (07:00 - 1 hour offset)
	offsetHours := time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour
	nextReport := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	nextReport = nextReport.Add(-offsetHours) // Subtract offset to get server time
	
	if pm.config.SummaryReportSchedule == "weekly" {
		daysUntilMonday := (8 - int(now.Weekday())) % 7
		if daysUntilMonday == 0 && now.After(nextReport) {
			daysUntilMonday = 7
		}
		nextReport = nextReport.AddDate(0, 0, daysUntilMonday)
	} else {
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
			
			// Display with offset for user convenience
			nextReportDisplay := nextReport.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
			msg := fmt.Sprintf("📅 Next summary report scheduled for: %s (in %s)", 
				nextReportDisplay.Format("2006-01-02 15:04:05"), formatDuration(duration))
			log.Printf(msg)
			pm.addLog(msg)
			
			time.Sleep(duration)
			
			log.Printf("📊 Generating %s summary report...", pm.config.SummaryReportSchedule)
			if err := pm.sendSummaryReport(); err != nil {
				log.Printf("❌ Failed to send summary report: %v", err)
			}
		}
	}()
}

// getRecentIncidents returns incidents from the last X hours for all targets
func (pm *PingMonitor) getRecentIncidents() []struct {
	TargetName    string
	TargetAddress string
	Timestamp     string
	EventType     string
	Description   string
	IsResolved    bool
	Duration      string
	Value         float64 // latency in ms or packet loss %
	Threshold     float64
} {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	type Incident struct {
		TargetName    string
		TargetAddress string
		Timestamp     string
		EventType     string
		Description   string
		IsResolved    bool
		Duration      string
		Time          time.Time // For sorting
		Value         float64   // latency in ms or packet loss %
		Threshold     float64
	}

	incidents := make([]Incident, 0)
	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)

	// Collect incidents from all targets
	for _, target := range pm.config.Targets {
		stats, exists := pm.targetStats[target.TargetAddr]
		if !exists {
			continue
		}

		// Track incidents and their resolutions
		type ProblemEvent struct {
			Timestamp   time.Time
			EventType   string
			Value       float64
			Threshold   float64
			Description string
			Resolved    bool
			Duration    time.Duration
		}
		
		problemEvents := make([]*ProblemEvent, 0) // Use slice to keep ALL incidents
		currentProblems := make(map[string]int) // Track index of unresolved problems by type

		// Go through recent events to find problems and resolutions
		for _, event := range stats.RecentEvents {
			// Only include events after cutoff time
			if event.Timestamp.Before(cutoffTime) {
				continue
			}

			// Check if it's a problem event
			if event.EventType == "down" || event.EventType == "high_latency" || event.EventType == "packet_loss" {
				// Create incident description
				var description string
				
				switch event.EventType {
				case "down":
					description = fmt.Sprintf("Target went DOWN")
				case "high_latency":
					description = fmt.Sprintf("High latency: %.2fms (threshold: %.0fms)", event.Value, event.Threshold)
				case "packet_loss":
					description = fmt.Sprintf("Packet loss: %.0f%% (threshold: %.0f%%)", event.Value, event.Threshold)
				}

				problem := &ProblemEvent{
					Timestamp:   event.Timestamp,
					EventType:   event.EventType,
					Value:       event.Value,
					Threshold:   event.Threshold,
					Description: description,
					Resolved:    false,
					Duration:    0,
				}
				problemEvents = append(problemEvents, problem)
				currentProblems[event.EventType] = len(problemEvents) - 1 // Track index of this problem
			}

			// Check if it's a recovery event and mark the corresponding problem as resolved
			if event.EventType == "up" {
				if idx, exists := currentProblems["down"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "down") // Remove from tracking
				}
			}
			if event.EventType == "latency_normal" {
				if idx, exists := currentProblems["high_latency"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "high_latency") // Remove from tracking
				}
			}
			if event.EventType == "packet_loss_normal" {
				if idx, exists := currentProblems["packet_loss"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "packet_loss") // Remove from tracking
				}
			}
		}

		// Add collected incidents
		for _, problem := range problemEvents {
			eventTime := problem.Timestamp.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
			
			durationStr := ""
			if problem.Resolved {
				durationStr = formatDuration(problem.Duration)
			}

			incidents = append(incidents, Incident{
				TargetName:    target.Name,
				TargetAddress: target.TargetAddr,
				Timestamp:     eventTime.Format("2006-01-02 15:04:05"),
				EventType:     problem.EventType,
				Description:   problem.Description,
				IsResolved:    problem.Resolved,
				Duration:      durationStr,
				Time:          problem.Timestamp,
				Value:         problem.Value,
				Threshold:     problem.Threshold,
			})
		}
	}

	// Sort by time (most recent first)
	sort.Slice(incidents, func(i, j int) bool {
		return incidents[i].Time.After(incidents[j].Time)
	})

	// Convert to return type (without Time field)
	result := make([]struct {
		TargetName    string
		TargetAddress string
		Timestamp     string
		EventType     string
		Description   string
		IsResolved    bool
		Duration      string
		Value         float64
		Threshold     float64
	}, len(incidents))

	for i, inc := range incidents {
		result[i].TargetName = inc.TargetName
		result[i].TargetAddress = inc.TargetAddress
		result[i].Timestamp = inc.Timestamp
		result[i].EventType = inc.EventType
		result[i].Description = inc.Description
		result[i].IsResolved = inc.IsResolved
		result[i].Duration = inc.Duration
		result[i].Value = inc.Value
		result[i].Threshold = inc.Threshold
	}

	return result
}

// getIncidentsSummary calculates summary statistics for recent incidents
func (pm *PingMonitor) getIncidentsSummary(hoursBack int) map[string]interface{} {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	cutoffTime := time.Now().Add(-time.Duration(hoursBack) * time.Hour)
	
	totalIncidents := 0
	resolvedCount := 0
	downtimeCount := 0
	highLatencyCount := 0
	packetLossCount := 0
	
	var totalDuration float64
	var firstIncidentTime time.Time
	var lastResolvedTime time.Time
	
	// Track durations by incident type for average calculation
	downtimeDurations := make([]float64, 0)
	highLatencyDurations := make([]float64, 0)
	packetLossDurations := make([]float64, 0)
	
	type ProblemEvent struct {
		Timestamp   time.Time
		EventType   string
		Value       float64
		Threshold   float64
		Description string
		Resolved    bool
		Duration    time.Duration
	}
	
	type TargetBreakdown struct {
		Name                     string
		TotalCount               int
		DownCount                int
		HighLatencyCount         int
		PacketLossCount          int
		ResolvedCount            int
		TotalDuration            float64 // in seconds
		AvgResolution            string
		DownDurations            []float64 // durations for down incidents
		HighLatencyDurations     []float64 // durations for high latency incidents
		PacketLossDurations      []float64 // durations for packet loss incidents
		AvgDownResolution        string
		AvgHighLatencyResolution string
		AvgPacketLossResolution  string
		// High latency metrics
		HighLatencyValues        []float64 // latency values in ms
		MinHighLatency           float64
		MaxHighLatency           float64
		AvgHighLatency           float64
		// Packet loss metrics
		PacketLossValues         []float64 // packet loss percentages
		MaxPacketLoss            float64
		AvgPacketLoss            float64
		// Time tracking
		FirstIncident            string
		LastResolved             string
		TotalDurationStr         string
	}
	
	targetBreakdowns := make(map[string]*TargetBreakdown)
	
	// Process all targets' events
	for _, target := range pm.config.Targets {
		stats, exists := pm.targetStats[target.TargetAddr]
		if !exists {
			continue
		}

		problemEvents := make([]*ProblemEvent, 0)
		currentProblems := make(map[string]int)

		for _, event := range stats.RecentEvents {
			if event.Timestamp.Before(cutoffTime) {
				continue
			}

			// Check if it's a problem event
			if event.EventType == "down" || event.EventType == "high_latency" || event.EventType == "packet_loss" {
				var description string
				switch event.EventType {
				case "down":
					description = "Target went DOWN"
				case "high_latency":
					description = fmt.Sprintf("High latency: %.2fms (threshold: %.0fms)", event.Value, event.Threshold)
				case "packet_loss":
					description = fmt.Sprintf("Packet loss: %.0f%% (threshold: %.0f%%)", event.Value, event.Threshold)
				}

				problem := &ProblemEvent{
					Timestamp:   event.Timestamp,
					EventType:   event.EventType,
					Value:       event.Value,
					Threshold:   event.Threshold,
					Description: description,
					Resolved:    false,
					Duration:    0,
				}
				problemEvents = append(problemEvents, problem)
				currentProblems[event.EventType] = len(problemEvents) - 1
			}

			// Check if it's a recovery event
			if event.EventType == "up" {
				if idx, exists := currentProblems["down"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "down")
				}
			}
			if event.EventType == "latency_normal" {
				if idx, exists := currentProblems["high_latency"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "high_latency")
				}
			}
			if event.EventType == "packet_loss_normal" {
				if idx, exists := currentProblems["packet_loss"]; exists && !problemEvents[idx].Resolved {
					problemEvents[idx].Resolved = true
					problemEvents[idx].Duration = event.Duration
					delete(currentProblems, "packet_loss")
				}
			}
		}

		// Count incidents by type and resolution status
		for _, problem := range problemEvents {
			totalIncidents++
			
			// Track first incident time (earliest)
			if firstIncidentTime.IsZero() || problem.Timestamp.Before(firstIncidentTime) {
				firstIncidentTime = problem.Timestamp
			}
			
			// Initialize target breakdown if not exists
			if targetBreakdowns[target.Name] == nil {
				targetBreakdowns[target.Name] = &TargetBreakdown{
					Name:                 target.Name,
					DownDurations:        make([]float64, 0),
					HighLatencyDurations: make([]float64, 0),
					PacketLossDurations:  make([]float64, 0),
					HighLatencyValues:    make([]float64, 0),
					PacketLossValues:     make([]float64, 0),
				}
			}
			
			// Track per-target first incident time (need to parse as we're comparing strings later)
			targetFirstTime := problem.Timestamp
			if targetBreakdowns[target.Name].FirstIncident == "" || problem.Timestamp.Before(targetFirstTime) {
				targetBreakdowns[target.Name].FirstIncident = problem.Timestamp.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour).Format("2006-01-02 15:04:05")
			}
			
			// Update target breakdown counts
			targetBreakdowns[target.Name].TotalCount++
			
			if problem.Resolved {
				resolvedCount++
				durationSeconds := problem.Duration.Seconds()
				totalDuration += durationSeconds
				
				// Track last resolved time (when the problem was resolved)
				resolvedTime := problem.Timestamp.Add(problem.Duration)
				if lastResolvedTime.IsZero() || resolvedTime.After(lastResolvedTime) {
					lastResolvedTime = resolvedTime
				}
				
				// Track per target
				targetBreakdowns[target.Name].ResolvedCount++
				targetBreakdowns[target.Name].TotalDuration += durationSeconds
				
				// Track per-target last resolved time
				targetBreakdowns[target.Name].LastResolved = resolvedTime.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour).Format("2006-01-02 15:04:05")
				
				// Track per incident type globally
				switch problem.EventType {
				case "down":
					downtimeDurations = append(downtimeDurations, durationSeconds)
					targetBreakdowns[target.Name].DownDurations = append(targetBreakdowns[target.Name].DownDurations, durationSeconds)
				case "high_latency":
					highLatencyDurations = append(highLatencyDurations, durationSeconds)
					targetBreakdowns[target.Name].HighLatencyDurations = append(targetBreakdowns[target.Name].HighLatencyDurations, durationSeconds)
				case "packet_loss":
					packetLossDurations = append(packetLossDurations, durationSeconds)
					targetBreakdowns[target.Name].PacketLossDurations = append(targetBreakdowns[target.Name].PacketLossDurations, durationSeconds)
				}
			}
			
			switch problem.EventType {
			case "down":
				downtimeCount++
				targetBreakdowns[target.Name].DownCount++
			case "high_latency":
				highLatencyCount++
				targetBreakdowns[target.Name].HighLatencyCount++
				// Track latency value in ms
				targetBreakdowns[target.Name].HighLatencyValues = append(targetBreakdowns[target.Name].HighLatencyValues, problem.Value)
			case "packet_loss":
				packetLossCount++
				targetBreakdowns[target.Name].PacketLossCount++
				// Track packet loss percentage
				targetBreakdowns[target.Name].PacketLossValues = append(targetBreakdowns[target.Name].PacketLossValues, problem.Value)
			}
		}
	}

	// Calculate average resolution time
	avgResolution := "N/A"
	if resolvedCount > 0 {
		avgSeconds := totalDuration / float64(resolvedCount)
		avgResolution = fmt.Sprintf("%.0fs", avgSeconds)
	}

	// Calculate resolution percentage
	resolvedPercent := 0
	if totalIncidents > 0 {
		resolvedPercent = (resolvedCount * 100) / totalIncidents
	}

	// Get all affected targets sorted by incident count (most affected first)
	topTargets := make([]*TargetBreakdown, 0)
	for _, breakdown := range targetBreakdowns {
		// Calculate avg resolution time for this target (overall)
		if breakdown.ResolvedCount > 0 {
			avgSeconds := breakdown.TotalDuration / float64(breakdown.ResolvedCount)
			breakdown.AvgResolution = fmt.Sprintf("%.0fs", avgSeconds)
		} else {
			breakdown.AvgResolution = "N/A"
		}
		
		// Calculate avg resolution time per incident type for this target
		if len(breakdown.DownDurations) > 0 {
			var sum float64
			for _, d := range breakdown.DownDurations {
				sum += d
			}
			breakdown.AvgDownResolution = fmt.Sprintf("%.0fs", sum/float64(len(breakdown.DownDurations)))
		} else {
			breakdown.AvgDownResolution = "N/A"
		}
		
		if len(breakdown.HighLatencyDurations) > 0 {
			var sum float64
			for _, d := range breakdown.HighLatencyDurations {
				sum += d
			}
			breakdown.AvgHighLatencyResolution = fmt.Sprintf("%.0fs", sum/float64(len(breakdown.HighLatencyDurations)))
		} else {
			breakdown.AvgHighLatencyResolution = "N/A"
		}
		
		if len(breakdown.PacketLossDurations) > 0 {
			var sum float64
			for _, d := range breakdown.PacketLossDurations {
				sum += d
			}
			breakdown.AvgPacketLossResolution = fmt.Sprintf("%.0fs", sum/float64(len(breakdown.PacketLossDurations)))
		} else {
			breakdown.AvgPacketLossResolution = "N/A"
		}
		
		// Calculate high latency metrics (min, max, avg in ms)
		if len(breakdown.HighLatencyValues) > 0 {
			breakdown.MinHighLatency = breakdown.HighLatencyValues[0]
			breakdown.MaxHighLatency = breakdown.HighLatencyValues[0]
			var sum float64
			for _, val := range breakdown.HighLatencyValues {
				if val < breakdown.MinHighLatency {
					breakdown.MinHighLatency = val
				}
				if val > breakdown.MaxHighLatency {
					breakdown.MaxHighLatency = val
				}
				sum += val
			}
			breakdown.AvgHighLatency = sum / float64(len(breakdown.HighLatencyValues))
		}
		
		// Calculate packet loss metrics (max, avg in %)
		if len(breakdown.PacketLossValues) > 0 {
			breakdown.MaxPacketLoss = breakdown.PacketLossValues[0]
			var sum float64
			for _, val := range breakdown.PacketLossValues {
				if val > breakdown.MaxPacketLoss {
					breakdown.MaxPacketLoss = val
				}
				sum += val
			}
			breakdown.AvgPacketLoss = sum / float64(len(breakdown.PacketLossValues))
		}
		
		// Calculate total duration string for this target
		if breakdown.ResolvedCount > 0 {
			breakdown.TotalDurationStr = formatDuration(time.Duration(breakdown.TotalDuration * float64(time.Second)))
		}
		
		topTargets = append(topTargets, breakdown)
	}
	sort.Slice(topTargets, func(i, j int) bool {
		return topTargets[i].TotalCount > topTargets[j].TotalCount
	})

	// Calculate average resolution time by incident type
	avgDowntimeResolution := "N/A"
	if len(downtimeDurations) > 0 {
		var sum float64
		for _, d := range downtimeDurations {
			sum += d
		}
		avgDowntimeResolution = fmt.Sprintf("%.0fs", sum/float64(len(downtimeDurations)))
	}
	
	avgHighLatencyResolution := "N/A"
	if len(highLatencyDurations) > 0 {
		var sum float64
		for _, d := range highLatencyDurations {
			sum += d
		}
		avgHighLatencyResolution = fmt.Sprintf("%.0fs", sum/float64(len(highLatencyDurations)))
	}
	
	avgPacketLossResolution := "N/A"
	if len(packetLossDurations) > 0 {
		var sum float64
		for _, d := range packetLossDurations {
			sum += d
		}
		avgPacketLossResolution = fmt.Sprintf("%.0fs", sum/float64(len(packetLossDurations)))
	}

	// Format first incident and last resolved times
	firstIncidentStr := ""
	if !firstIncidentTime.IsZero() {
		firstIncidentStr = firstIncidentTime.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour).Format("2006-01-02 15:04:05")
	}
	
	lastResolvedStr := ""
	if !lastResolvedTime.IsZero() {
		lastResolvedStr = lastResolvedTime.Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour).Format("2006-01-02 15:04:05")
	}
	
	// Calculate total duration as sum of all resolved incident durations
	totalDurationStr := ""
	if resolvedCount > 0 {
		// totalDuration already contains the sum of all resolved incident durations
		totalDurationStr = formatDuration(time.Duration(totalDuration * float64(time.Second)))
	}

	return map[string]interface{}{
		"TotalIncidents":           totalIncidents,
		"ResolvedCount":            resolvedCount,
		"ResolvedPercent":          resolvedPercent,
		"DowntimeCount":            downtimeCount,
		"HighLatencyCount":         highLatencyCount,
		"PacketLossCount":          packetLossCount,
		"AvgResolution":            avgResolution,
		"AvgDowntimeResolution":    avgDowntimeResolution,
		"AvgHighLatencyResolution": avgHighLatencyResolution,
		"AvgPacketLossResolution":  avgPacketLossResolution,
		"FirstIncident":            firstIncidentStr,
		"LastResolved":             lastResolvedStr,
		"TotalDuration":            totalDurationStr,
		"TopTargets":               topTargets,
	}
}
