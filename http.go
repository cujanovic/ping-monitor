package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// HTTPRateLimiter methods

// Allow checks if a request from the given IP is allowed
func (rl *HTTPRateLimiter) Allow(ip string) bool {
	if rl == nil {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	requests := rl.requests[ip]
	validRequests := make([]time.Time, 0, len(requests))
	for _, t := range requests {
		if t.After(cutoff) {
			validRequests = append(validRequests, t)
		}
	}

	if len(validRequests) >= rl.limit {
		rl.requests[ip] = validRequests
		return false
	}

	validRequests = append(validRequests, now)
	rl.requests[ip] = validRequests
	return true
}

// Cleanup removes old IP entries
func (rl *HTTPRateLimiter) Cleanup() {
	if rl == nil {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window * 2)

	for ip, requests := range rl.requests {
		allOld := true
		for _, t := range requests {
			if t.After(cutoff) {
				allOld = false
				break
			}
		}
		if allOld {
			delete(rl.requests, ip)
		}
	}
}

// initTemplates initializes HTML templates from disk
func initTemplates() *template.Template {
	tmpl, err := template.ParseGlob("templates/*.html")
	if err != nil {
		log.Fatalf("❌ Failed to load templates: %v", err)
	}
	return tmpl
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// securityHeadersMiddleware adds security headers to responses
func securityHeadersMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Content Security Policy - allow scripts from same origin and Chart.js CDN
		w.Header().Set("Content-Security-Policy", 
			"default-src 'self'; "+
			"script-src 'self'; "+
			"script-src-elem 'self'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"font-src 'self'; "+
			"connect-src 'self'; "+
			"frame-ancestors 'none'; "+
			"base-uri 'self'; "+
			"form-action 'self'")
		
		// Additional security headers
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		
		next(w, r)
	}
}

// rateLimitMiddleware wraps a handler with rate limiting
func (pm *PingMonitor) rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pm.httpRateLimiter != nil {
			ip := getClientIP(r)
			if !pm.httpRateLimiter.Allow(ip) {
				http.Error(w, "Rate limit exceeded. Please try again later.", http.StatusTooManyRequests)
				log.Printf("⚠️  Rate limit exceeded for IP: %s", ip)
				return
			}
		}
		next(w, r)
	}
}

// handleRoot handles the root endpoint
func (pm *PingMonitor) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	uptime := time.Since(pm.serviceStartTime) // Use serviceStartTime for actual uptime
	schedule := fmt.Sprintf("%s at %s", strings.Title(pm.config.SummaryReportSchedule), pm.config.SummaryReportTime)
	
	// Build targets list with status
	type TargetInfo struct {
		Name              string
		Address           string
		Label             string
		IsDown            bool
		IsSlow            bool
		HasPacketLoss     bool
		LatencyMs         float64 // Latest ping latency in ms
		FailedChecks      int64   // Total down checks
		HighLatencyChecks int64   // Total high latency checks
		PacketLossChecks  int64   // Total packet loss checks
		TotalFailedChecks int64   // Sum of all failed checks
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	var totalChecks, successfulChecks int64
	var totalUptime float64
	healthyTargets := make([]TargetInfo, 0, len(pm.config.Targets))
	issueTargets := make([]TargetInfo, 0)
	criticalTargets := make([]TargetInfo, 0)
	statsStartTime := pm.statsStartTime
	
	for i, target := range pm.config.Targets {
		stats := pm.targetStats[target.TargetAddr]
		failedChecks := int64(0)
		highLatencyChecks := int64(0)
		packetLossChecks := int64(0)
		totalChecksTarget := int64(0)
		successfulChecksTarget := int64(0)
		
		if stats != nil {
			failedChecks = stats.FailedChecks
			highLatencyChecks = stats.HighLatencyCount
			packetLossChecks = stats.PacketLossEvents
			totalChecksTarget = stats.TotalChecks
			successfulChecksTarget = stats.SuccessfulChecks
		}
		
		targetInfo := TargetInfo{
			Name:              target.Name,
			Address:           target.TargetAddr,
			Label:             getTargetLabel(target.TargetAddr),
			IsDown:            pm.downTargets[target.TargetAddr],
			IsSlow:            pm.slowTargets[target.TargetAddr],
			HasPacketLoss:     pm.packetLossTargets[target.TargetAddr],
			LatencyMs:         pm.lastLatency[target.TargetAddr],
			FailedChecks:      failedChecks,
			HighLatencyChecks: highLatencyChecks,
			PacketLossChecks:  packetLossChecks,
			TotalFailedChecks: failedChecks + highLatencyChecks + packetLossChecks,
		}
		targets[i] = targetInfo
		
		// Calculate health statistics while we have the lock
		if totalChecksTarget > 0 {
			uptimePercent := (float64(successfulChecksTarget) / float64(totalChecksTarget)) * 100
			totalChecks += totalChecksTarget
			successfulChecks += successfulChecksTarget
			totalUptime += uptimePercent
			
			if uptimePercent >= 99.0 {
				healthyTargets = append(healthyTargets, targetInfo)
			} else if uptimePercent >= 95.0 {
				issueTargets = append(issueTargets, targetInfo)
			} else {
				criticalTargets = append(criticalTargets, targetInfo)
			}
		}
	}
	pm.mu.RUnlock()
	
	// Get recent incidents
	incidents := pm.getRecentIncidents()
	summary := pm.getIncidentsSummary(pm.config.RecentIncidentsHours)
	
	// Calculate final statistics
	avgUptime := 0.0
	if len(pm.config.Targets) > 0 {
		avgUptime = totalUptime / float64(len(pm.config.Targets))
	}
	successRate := 0.0
	if totalChecks > 0 {
		successRate = (float64(successfulChecks) / float64(totalChecks)) * 100
	}
	
	data := struct {
		TargetCount      int
		Uptime           string
		Interval         int
		Schedule         string
		Timestamp        string
		Targets          []TargetInfo
		RecentIncidents  []struct {
			TargetName    string
			TargetAddress string
			Timestamp     string
			EventType     string
			Description   string
			IsResolved    bool
			Duration      string
			Value         float64
			Threshold     float64
		}
		IncidentsHours     int
		IncidentsSummary   map[string]interface{}
		HealthyTargets     []TargetInfo
		IssueTargets       []TargetInfo
		CriticalTargets    []TargetInfo
		AvgUptime          float64
		TotalChecks        int64
		SuccessfulChecks   int64
		SuccessRate        float64
		StatsStartTime     string
		ServiceStartTime   string
	}{
		TargetCount:      len(pm.config.Targets),
		Uptime:           formatDuration(uptime),
		Interval:         pm.config.PingIntervalSeconds,
		Schedule:         schedule,
		Timestamp:        pm.getReportTime().Format("2006-01-02 15:04:05"),
		Targets:          targets,
		RecentIncidents:  incidents,
		IncidentsHours:   pm.config.RecentIncidentsHours,
		IncidentsSummary: summary,
		HealthyTargets:   healthyTargets,
		IssueTargets:     issueTargets,
		CriticalTargets:  criticalTargets,
		AvgUptime:        avgUptime,
		TotalChecks:      totalChecks,
		SuccessfulChecks: successfulChecks,
		SuccessRate:      successRate,
		StatsStartTime:   statsStartTime.Format("Jan 2 15:04"),
		ServiceStartTime: pm.serviceStartTime.Format("Jan 2 15:04"),
	}
	
	if err := pm.templates.ExecuteTemplate(w, "root.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleStatus handles the status endpoint
func (pm *PingMonitor) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "OK\n")
}

// handleStaticJS serves the JavaScript file
func (pm *PingMonitor) handleStaticJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	
	// Read the JavaScript file from templates directory
	jsContent, err := os.ReadFile("templates/app.js")
	if err != nil {
		http.Error(w, "JavaScript file not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read app.js: %v", err)
		return
	}
	
	w.Write(jsContent)
}

// handleStaticFavicon serves the favicon SVG file
func (pm *PingMonitor) handleStaticFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	
	// Read the favicon SVG file from templates directory
	faviconContent, err := os.ReadFile("templates/favicon.svg")
	if err != nil {
		http.Error(w, "Favicon not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read favicon.svg: %v", err)
		return
	}
	
	w.Write(faviconContent)
}

// handleStaticChartJS serves the Chart.js library
func (pm *PingMonitor) handleStaticChartJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	
	content, err := os.ReadFile("templates/chart.min.js")
	if err != nil {
		http.Error(w, "Chart.js not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read chart.min.js: %v", err)
		return
	}
	
	w.Write(content)
}

// handleStaticChartAdapter serves the Chart.js date adapter
func (pm *PingMonitor) handleStaticChartAdapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	
	content, err := os.ReadFile("templates/chartjs-adapter-date-fns.min.js")
	if err != nil {
		http.Error(w, "Chart adapter not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read chartjs-adapter-date-fns.min.js: %v", err)
		return
	}
	
	w.Write(content)
}

// handleStaticGraphsJS serves the graphs page JavaScript
func (pm *PingMonitor) handleStaticGraphsJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // Don't cache during development
	
	content, err := os.ReadFile("templates/graphs.js")
	if err != nil {
		http.Error(w, "Graphs JS not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read graphs.js: %v", err)
		return
	}
	
	w.Write(content)
}

// handleStaticHammerJS serves the Hammer.js library for touch gestures
func (pm *PingMonitor) handleStaticHammerJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	
	content, err := os.ReadFile("templates/hammer.min.js")
	if err != nil {
		http.Error(w, "Hammer.js not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read hammer.min.js: %v", err)
		return
	}
	
	w.Write(content)
}

// handleStaticZoomPlugin serves the Chart.js zoom plugin
func (pm *PingMonitor) handleStaticZoomPlugin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	
	content, err := os.ReadFile("templates/chartjs-plugin-zoom.min.js")
	if err != nil {
		http.Error(w, "Zoom plugin not found", http.StatusNotFound)
		log.Printf("⚠️  Failed to read chartjs-plugin-zoom.min.js: %v", err)
		return
	}
	
	w.Write(content)
}

// handleReports handles the reports page
func (pm *PingMonitor) handleReports(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	data := struct {
		Timestamp string
	}{
		Timestamp: pm.getReportTime().Format("2006-01-02 15:04:05"),
	}
	
	if err := pm.templates.ExecuteTemplate(w, "reports.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleReportNow handles the current state report
func (pm *PingMonitor) handleReportNow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	logs := pm.getRecentLogs()
	
	pm.mu.RLock()
	downCount := len(pm.downTargets)
	slowCount := len(pm.slowTargets)
	packetLossCount := len(pm.packetLossTargets)
	pm.mu.RUnlock()
	
	getClass := func(count int) string {
		if count > 0 {
			return "status-error"
		}
		return "status-good"
	}
	
	getWarningClass := func(count int) string {
		if count > 0 {
			return "status-warning"
		}
		return "status-good"
	}
	
	type FormattedLog struct {
		Timestamp string
		Message   string
	}
	formattedLogs := make([]FormattedLog, len(logs))
	for i, log := range logs {
		formattedLogs[i] = FormattedLog{
			Timestamp: log.Timestamp.Format("2006-01-02 15:04:05"),
			Message:   log.Message,
		}
	}
	
	// Build targets list with status
	type TargetInfo struct {
		Name              string
		Address           string
		Label             string
		IsDown            bool
		IsSlow            bool
		HasPacketLoss     bool
		LatencyMs         float64 // Latest ping latency in ms
		FailedChecks      int64   // Total down checks
		HighLatencyChecks int64   // Total high latency checks
		PacketLossChecks  int64   // Total packet loss checks
		TotalFailedChecks int64   // Sum of all failed checks
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	var totalChecks, successfulChecks int64
	var totalUptime float64
	healthyTargets := make([]TargetInfo, 0, len(pm.config.Targets))
	issueTargets := make([]TargetInfo, 0)
	criticalTargets := make([]TargetInfo, 0)
	statsStartTime := pm.statsStartTime
	
	for i, target := range pm.config.Targets {
		stats := pm.targetStats[target.TargetAddr]
		failedChecks := int64(0)
		highLatencyChecks := int64(0)
		packetLossChecks := int64(0)
		totalChecksTarget := int64(0)
		successfulChecksTarget := int64(0)
		
		if stats != nil {
			failedChecks = stats.FailedChecks
			highLatencyChecks = stats.HighLatencyCount
			packetLossChecks = stats.PacketLossEvents
			totalChecksTarget = stats.TotalChecks
			successfulChecksTarget = stats.SuccessfulChecks
		}
		
		targetInfo := TargetInfo{
			Name:              target.Name,
			Address:           target.TargetAddr,
			Label:             getTargetLabel(target.TargetAddr),
			IsDown:            pm.downTargets[target.TargetAddr],
			IsSlow:            pm.slowTargets[target.TargetAddr],
			HasPacketLoss:     pm.packetLossTargets[target.TargetAddr],
			LatencyMs:         pm.lastLatency[target.TargetAddr],
			FailedChecks:      failedChecks,
			HighLatencyChecks: highLatencyChecks,
			PacketLossChecks:  packetLossChecks,
			TotalFailedChecks: failedChecks + highLatencyChecks + packetLossChecks,
		}
		targets[i] = targetInfo
		
		// Calculate health statistics while we have the lock
		if totalChecksTarget > 0 {
			uptimePercent := (float64(successfulChecksTarget) / float64(totalChecksTarget)) * 100
			totalChecks += totalChecksTarget
			successfulChecks += successfulChecksTarget
			totalUptime += uptimePercent
			
			if uptimePercent >= 99.0 {
				healthyTargets = append(healthyTargets, targetInfo)
			} else if uptimePercent >= 95.0 {
				issueTargets = append(issueTargets, targetInfo)
			} else {
				criticalTargets = append(criticalTargets, targetInfo)
			}
		}
	}
	pm.mu.RUnlock()
	
	// Get recent incidents
	incidents := pm.getRecentIncidents()
	summary := pm.getIncidentsSummary(pm.config.RecentIncidentsHours)
	
	// Calculate final statistics
	avgUptime := 0.0
	if len(pm.config.Targets) > 0 {
		avgUptime = totalUptime / float64(len(pm.config.Targets))
	}
	successRate := 0.0
	if totalChecks > 0 {
		successRate = (float64(successfulChecks) / float64(totalChecks)) * 100
	}
	
	data := struct {
		DownCount          int
		SlowCount          int
		PacketLossCount    int
		TotalTargets       int
		Timestamp          string
		LogCount           int
		Logs               []FormattedLog
		DownClass          string
		SlowClass          string
		PacketLossClass    string
		Targets            []TargetInfo
		RecentIncidents    []struct {
			TargetName    string
			TargetAddress string
			Timestamp     string
			EventType     string
			Description   string
			IsResolved    bool
			Duration      string
			Value         float64
			Threshold     float64
		}
		IncidentsHours     int
		IncidentsSummary   map[string]interface{}
		HealthyTargets     []TargetInfo
		IssueTargets       []TargetInfo
		CriticalTargets    []TargetInfo
		AvgUptime          float64
		TotalChecks        int64
		SuccessfulChecks   int64
		SuccessRate        float64
		StatsStartTime     string
		ServiceStartTime   string
	}{
		DownCount:        downCount,
		SlowCount:        slowCount,
		PacketLossCount:  packetLossCount,
		TotalTargets:     len(pm.config.Targets),
		Timestamp:        pm.getReportTime().Format("2006-01-02 15:04:05"),
		LogCount:         pm.config.HTTPLogLines,
		Logs:             formattedLogs,
		DownClass:        getClass(downCount),
		SlowClass:        getWarningClass(slowCount),
		PacketLossClass:  getWarningClass(packetLossCount),
		Targets:          targets,
		RecentIncidents:  incidents,
		IncidentsHours:   pm.config.RecentIncidentsHours,
		IncidentsSummary: summary,
		HealthyTargets:   healthyTargets,
		IssueTargets:     issueTargets,
		CriticalTargets:  criticalTargets,
		AvgUptime:        avgUptime,
		TotalChecks:      totalChecks,
		SuccessfulChecks: successfulChecks,
		SuccessRate:      successRate,
		StatsStartTime:   statsStartTime.Format("Jan 2 15:04"),
		ServiceStartTime: pm.serviceStartTime.Format("Jan 2 15:04"),
	}
	
	if err := pm.templates.ExecuteTemplate(w, "report_now.html", data); err != nil{
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleReportAll handles the full report page
func (pm *PingMonitor) handleReportAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	logs := pm.getRecentLogs()
	
	pm.mu.RLock()
	downCount := len(pm.downTargets)
	slowCount := len(pm.slowTargets)
	packetLossCount := len(pm.packetLossTargets)
	pm.mu.RUnlock()
	
	pm.lastEmailReportMu.RLock()
	emailReport := pm.lastEmailReport
	pm.lastEmailReportMu.RUnlock()
	
	var allReportsContent []ReportWithContent
	if pm.config.ReportsDirectory != "" {
		reportFiles := pm.getAllReports()
		for _, filename := range reportFiles {
			filepath := fmt.Sprintf("%s/%s", pm.config.ReportsDirectory, filename)
			data, err := os.ReadFile(filepath)
			if err == nil {
				allReportsContent = append(allReportsContent, ReportWithContent{
					Filename: filename,
					Content:  string(data),
				})
			}
		}
	}
	
	getClass := func(count int) string {
		if count > 0 {
			return "status-error"
		}
		return "status-good"
	}
	
	getWarningClass := func(count int) string {
		if count > 0 {
			return "status-warning"
		}
		return "status-good"
	}
	
	type FormattedLog struct {
		Timestamp string
		Message   string
	}
	formattedLogs := make([]FormattedLog, len(logs))
	for i, log := range logs {
		formattedLogs[i] = FormattedLog{
			Timestamp: log.Timestamp.Format("2006-01-02 15:04:05"),
			Message:   log.Message,
		}
	}
	
	// Build targets list with status
	type TargetInfo struct {
		Name              string
		Address           string
		Label             string
		IsDown            bool
		IsSlow            bool
		HasPacketLoss     bool
		LatencyMs         float64 // Latest ping latency in ms
		FailedChecks      int64   // Total down checks
		HighLatencyChecks int64   // Total high latency checks
		PacketLossChecks  int64   // Total packet loss checks
		TotalFailedChecks int64   // Sum of all failed checks
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	var totalChecks, successfulChecks int64
	var totalUptime float64
	healthyTargets := make([]TargetInfo, 0, len(pm.config.Targets))
	issueTargets := make([]TargetInfo, 0)
	criticalTargets := make([]TargetInfo, 0)
	statsStartTime := pm.statsStartTime
	
	for i, target := range pm.config.Targets {
		stats := pm.targetStats[target.TargetAddr]
		failedChecks := int64(0)
		highLatencyChecks := int64(0)
		packetLossChecks := int64(0)
		totalChecksTarget := int64(0)
		successfulChecksTarget := int64(0)
		
		if stats != nil {
			failedChecks = stats.FailedChecks
			highLatencyChecks = stats.HighLatencyCount
			packetLossChecks = stats.PacketLossEvents
			totalChecksTarget = stats.TotalChecks
			successfulChecksTarget = stats.SuccessfulChecks
		}
		
		targetInfo := TargetInfo{
			Name:              target.Name,
			Address:           target.TargetAddr,
			Label:             getTargetLabel(target.TargetAddr),
			IsDown:            pm.downTargets[target.TargetAddr],
			IsSlow:            pm.slowTargets[target.TargetAddr],
			HasPacketLoss:     pm.packetLossTargets[target.TargetAddr],
			LatencyMs:         pm.lastLatency[target.TargetAddr],
			FailedChecks:      failedChecks,
			HighLatencyChecks: highLatencyChecks,
			PacketLossChecks:  packetLossChecks,
			TotalFailedChecks: failedChecks + highLatencyChecks + packetLossChecks,
		}
		targets[i] = targetInfo
		
		// Calculate health statistics while we have the lock
		if totalChecksTarget > 0 {
			uptimePercent := (float64(successfulChecksTarget) / float64(totalChecksTarget)) * 100
			totalChecks += totalChecksTarget
			successfulChecks += successfulChecksTarget
			totalUptime += uptimePercent
			
			if uptimePercent >= 99.0 {
				healthyTargets = append(healthyTargets, targetInfo)
			} else if uptimePercent >= 95.0 {
				issueTargets = append(issueTargets, targetInfo)
			} else {
				criticalTargets = append(criticalTargets, targetInfo)
			}
		}
	}
	pm.mu.RUnlock()
	
	// Get recent incidents
	incidents := pm.getRecentIncidents()
	summary := pm.getIncidentsSummary(pm.config.RecentIncidentsHours)
	
	// Calculate final statistics
	avgUptime := 0.0
	if len(pm.config.Targets) > 0 {
		avgUptime = totalUptime / float64(len(pm.config.Targets))
	}
	successRate := 0.0
	if totalChecks > 0 {
		successRate = (float64(successfulChecks) / float64(totalChecks)) * 100
	}
	
	data := struct {
		DownCount          int
		SlowCount          int
		PacketLossCount    int
		TotalTargets       int
		Timestamp          string
		LogCount           int
		Logs               []FormattedLog
		DownClass          string
		SlowClass          string
		PacketLossClass    string
		EmailReport        string
		Schedule           string
		AllReports         []ReportWithContent
		ReportsDir         string
		Targets            []TargetInfo
		RecentIncidents    []struct {
			TargetName    string
			TargetAddress string
			Timestamp     string
			EventType     string
			Description   string
			IsResolved    bool
			Duration      string
			Value         float64
			Threshold     float64
		}
		IncidentsHours     int
		IncidentsSummary   map[string]interface{}
		HealthyTargets     []TargetInfo
		IssueTargets       []TargetInfo
		CriticalTargets    []TargetInfo
		AvgUptime          float64
		TotalChecks        int64
		SuccessfulChecks   int64
		SuccessRate        float64
		StatsStartTime     string
		ServiceStartTime   string
	}{
		DownCount:        downCount,
		SlowCount:        slowCount,
		PacketLossCount:  packetLossCount,
		TotalTargets:     len(pm.config.Targets),
		Timestamp:        pm.getReportTime().Format("2006-01-02 15:04:05"),
		LogCount:         pm.config.HTTPLogLines,
		Logs:             formattedLogs,
		DownClass:        getClass(downCount),
		SlowClass:        getWarningClass(slowCount),
		PacketLossClass:  getWarningClass(packetLossCount),
		EmailReport:      emailReport,
		Schedule:         pm.config.SummaryReportSchedule,
		AllReports:       allReportsContent,
		ReportsDir:       pm.config.ReportsDirectory,
		Targets:          targets,
		RecentIncidents:  incidents,
		IncidentsHours:   pm.config.RecentIncidentsHours,
		IncidentsSummary: summary,
		HealthyTargets:   healthyTargets,
		IssueTargets:     issueTargets,
		CriticalTargets:  criticalTargets,
		AvgUptime:        avgUptime,
		TotalChecks:      totalChecks,
		SuccessfulChecks: successfulChecks,
		SuccessRate:      successRate,
		StatsStartTime:   statsStartTime.Format("Jan 2 15:04"),
		ServiceStartTime: pm.serviceStartTime.Format("Jan 2 15:04"),
	}
	
	if err := pm.templates.ExecuteTemplate(w, "report_all.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleReportsGraphs handles the graphs page
func (pm *PingMonitor) handleReportsGraphs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	uptime := time.Since(pm.serviceStartTime)
	schedule := fmt.Sprintf("%s at %s", strings.Title(pm.config.SummaryReportSchedule), pm.config.SummaryReportTime)
	
	// Build targets list for dropdown
	type TargetInfo struct {
		Name    string
		Address string
		Label   string
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	for i, target := range pm.config.Targets {
		targets[i] = TargetInfo{
			Name:    target.Name,
			Address: target.TargetAddr,
			Label:   getTargetLabel(target.TargetAddr),
		}
	}
	pm.mu.RUnlock()
	
	data := struct {
		TargetCount      int
		Uptime           string
		Interval         int
		Schedule         string
		Timestamp        string
		Targets          []TargetInfo
		ServiceStartTime string
	}{
		TargetCount:      len(pm.config.Targets),
		Uptime:           formatDuration(uptime),
		Interval:         pm.config.PingIntervalSeconds,
		Schedule:         schedule,
		Timestamp:        pm.getReportTime().Format("2006-01-02 15:04:05"),
		Targets:          targets,
		ServiceStartTime: pm.serviceStartTime.Format("Jan 2 15:04"),
	}
	
	if err := pm.templates.ExecuteTemplate(w, "report_graphs.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
	}
}

// handleAPILatencyHistory returns latency history data as JSON
func (pm *PingMonitor) handleAPILatencyHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	// Get hours parameter (default 24, max 1176 = 7 weeks)
	hours := 24
	if hoursParam := r.URL.Query().Get("hours"); hoursParam != "" {
		fmt.Sscanf(hoursParam, "%d", &hours)
		if hours < 1 {
			hours = 1
		}
		if hours > 1176 { // Max 7 weeks (49 days * 24 hours)
			hours = 1176
		}
	}
	
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	
	// Build response
	type TargetData struct {
		Name    string         `json:"name"`
		Address string         `json:"address"`
		Label   string         `json:"label"`
		Points  []LatencyPoint `json:"points"`
	}
	
	pm.mu.RLock()
	targets := make([]TargetData, 0, len(pm.config.Targets))
	
	for _, target := range pm.config.Targets {
		points := pm.latencyHistory[target.TargetAddr]
		filteredPoints := make([]LatencyPoint, 0, len(points))
		
		for _, point := range points {
			if point.Timestamp.After(cutoff) {
				filteredPoints = append(filteredPoints, point)
			}
		}
		
		// Sort by timestamp
		sort.Slice(filteredPoints, func(i, j int) bool {
			return filteredPoints[i].Timestamp.Before(filteredPoints[j].Timestamp)
		})
		
		targets = append(targets, TargetData{
			Name:    target.Name,
			Address: target.TargetAddr,
			Label:   getTargetLabel(target.TargetAddr),
			Points:  filteredPoints,
		})
	}
	pm.mu.RUnlock()
	
	response := struct {
		Hours   int          `json:"hours"`
		Targets []TargetData `json:"targets"`
	}{
		Hours:   hours,
		Targets: targets,
	}
	
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("⚠️  JSON encode error: %v", err)
	}
}

// startHTTPServer starts the HTTP server
func (pm *PingMonitor) startHTTPServer() {
	if !pm.config.HTTPEnabled {
		return
	}

	// Public routes (no auth required, with security headers)
	http.HandleFunc("/status", securityHeadersMiddleware(pm.handleStatus))
	http.HandleFunc("/login", securityHeadersMiddleware(pm.handleLogin))
	http.HandleFunc("/logout", securityHeadersMiddleware(pm.handleLogout))
	http.HandleFunc("/static/app.js", securityHeadersMiddleware(pm.handleStaticJS))
	http.HandleFunc("/static/favicon.svg", securityHeadersMiddleware(pm.handleStaticFavicon))
	http.HandleFunc("/static/chart.min.js", securityHeadersMiddleware(pm.handleStaticChartJS))
	http.HandleFunc("/static/chartjs-adapter-date-fns.min.js", securityHeadersMiddleware(pm.handleStaticChartAdapter))
	http.HandleFunc("/static/graphs.js", securityHeadersMiddleware(pm.handleStaticGraphsJS))
	http.HandleFunc("/static/hammer.min.js", securityHeadersMiddleware(pm.handleStaticHammerJS))
	http.HandleFunc("/static/chartjs-plugin-zoom.min.js", securityHeadersMiddleware(pm.handleStaticZoomPlugin))
	
	// Protected routes (require auth if enabled, with security headers)
	http.HandleFunc("/", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleRoot))))
	http.HandleFunc("/reports", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReports))))
	http.HandleFunc("/report_now", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReportNow))))
	http.HandleFunc("/report_all", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReportAll))))
	http.HandleFunc("/reports/graphs", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReportsGraphs))))
	http.HandleFunc("/api/latency-history", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleAPILatencyHistory))))
	
	if pm.httpRateLimiter != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				pm.httpRateLimiter.Cleanup()
			}
		}()
	}

	go func() {
		log.Printf("🌐 Starting HTTP server on %s", pm.config.HTTPListen)
		pm.addLog(fmt.Sprintf("Starting HTTP server on %s", pm.config.HTTPListen))
		
		if err := http.ListenAndServe(pm.config.HTTPListen, nil); err != nil {
			// Fatal error - exit so systemd can restart the service (network may not be ready)
			log.Fatalf("❌ Failed to start HTTP server on %s: %v", pm.config.HTTPListen, err)
		}
	}()
}
