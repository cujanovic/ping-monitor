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
	
	if len(stats.RecentEvents) > 50 {
		stats.RecentEvents = stats.RecentEvents[len(stats.RecentEvents)-50:]
	}
}

// addLog adds a log entry to the pending buffer
func (pm *PingMonitor) addLog(message string) {
	entry := LogEntry{
		Timestamp: pm.getReportTime(),
		Message:   message,
	}

	pm.logMu.Lock()
	pm.logPendingBuffer = append(pm.logPendingBuffer, entry)
	pm.logMu.Unlock()
}

// logBufferFlusher periodically flushes pending logs
func (pm *PingMonitor) logBufferFlusher() {
	ticker := time.NewTicker(time.Duration(pm.config.LogBufferFlushSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pm.flushLogBuffer()
	}
}

// flushLogBuffer moves pending logs to the main buffer
func (pm *PingMonitor) flushLogBuffer() {
	pm.logMu.Lock()
	defer pm.logMu.Unlock()

	if len(pm.logPendingBuffer) == 0 {
		return
	}

	pm.logBuffer = append(pm.logBuffer, pm.logPendingBuffer...)
	
	if len(pm.logBuffer) > pm.config.HTTPLogLines {
		pm.logBuffer = pm.logBuffer[len(pm.logBuffer)-pm.config.HTTPLogLines:]
	}
	
	pm.logPendingBuffer = pm.logPendingBuffer[:0]
}

// getRecentLogs returns the recent log entries
func (pm *PingMonitor) getRecentLogs() []LogEntry {
	pm.flushLogBuffer()
	
	pm.logMu.Lock()
	defer pm.logMu.Unlock()

	logs := make([]LogEntry, len(pm.logBuffer))
	copy(logs, pm.logBuffer)
	return logs
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
	now := time.Now()
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

// buildReportBody builds the summary report body (continued in next message due to length)
func (pm *PingMonitor) buildReportBody(body *strings.Builder, schedule string, reportDuration time.Duration,
	reportStart, now time.Time, targetCount int, healthyTargets, issueTargets, criticalTargets []TargetReport,
	avgUptime float64, totalChecks, successfulChecks int64) {
	
	body.WriteString(fmt.Sprintf("📊 Ping Monitor %s Summary Report\n", strings.Title(schedule)))
	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString(fmt.Sprintf("Report Period: %s (%s - %s)\n", 
		formatDuration(reportDuration),
		reportStart.Format("Jan 2 15:04"),
		now.Format("Jan 2 15:04")))
	body.WriteString(fmt.Sprintf("Total Targets Monitored: %d\n\n", targetCount))
	
	body.WriteString("📈 OVERALL HEALTH\n")
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

	if len(healthyTargets) > 0 {
		pm.writeTargetDetails(body, "🟢 HEALTHY TARGETS (99%+ uptime)", healthyTargets)
	}
	if len(issueTargets) > 0 {
		pm.writeTargetDetails(body, "🟡 TARGETS WITH ISSUES (95-99% uptime)", issueTargets)
	}
	if len(criticalTargets) > 0 {
		pm.writeTargetDetails(body, "🔴 CRITICAL TARGETS (<95% uptime)", criticalTargets)
	}

	body.WriteString(strings.Repeat("━", 60) + "\n\n")
	body.WriteString(fmt.Sprintf("Next %s report: %s\n", schedule, pm.getNextReportTime().Format("Jan 2, 2006 15:04")))
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
				
				body.WriteString(fmt.Sprintf("      - %s: %d incidents%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
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
					
					body.WriteString(fmt.Sprintf("      - %s: %d incidents%s\n", report.Target.Name, report.TotalIssues, incidentBreakdown))
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
		body.WriteString(fmt.Sprintf("  ⚠️  Total Incidents: %d (%d high latency, %d packet loss, %d failed)\n", 
			report.TotalIssues, report.Stats.HighLatencyCount, report.Stats.PacketLossEvents, report.Stats.FailedChecks))
		
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

// getNextReportTime calculates the next report time
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

	nextReport := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	
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
			
			msg := fmt.Sprintf("📅 Next summary report scheduled for: %s (in %s)", 
				nextReport.Format("2006-01-02 15:04:05"), formatDuration(duration))
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
