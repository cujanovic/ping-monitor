package main

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
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
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ips := strings.Split(forwarded, ",")
		return strings.TrimSpace(ips[0])
	}
	
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// securityHeadersMiddleware adds security headers to responses
func securityHeadersMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Content Security Policy - allow scripts, styles, and images from same origin
		w.Header().Set("Content-Security-Policy", 
			"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"font-src 'self'; "+
			"connect-src 'self'; "+
			"frame-ancestors 'none'; "+
			"base-uri 'self'; "+
			"form-action 'self'; "+
			"require-trusted-types-for 'script'")
		
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
	
	uptime := time.Since(pm.statsStartTime)
	schedule := fmt.Sprintf("%s at %s", strings.Title(pm.config.SummaryReportSchedule), pm.config.SummaryReportTime)
	
	// Build targets list with status
	type TargetInfo struct {
		Name          string
		Address       string
		Label         string
		IsDown        bool
		IsSlow        bool
		HasPacketLoss bool
		LatencyMs     float64 // Latest ping latency in ms
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	for i, target := range pm.config.Targets {
		targets[i] = TargetInfo{
			Name:          target.Name,
			Address:       target.TargetAddr,
			Label:         getTargetLabel(target.TargetAddr),
			IsDown:        pm.downTargets[target.TargetAddr],
			IsSlow:        pm.slowTargets[target.TargetAddr],
			HasPacketLoss: pm.packetLossTargets[target.TargetAddr],
			LatencyMs:     pm.lastLatency[target.TargetAddr],
		}
	}
	pm.mu.RUnlock()
	
	data := struct {
		TargetCount int
		Uptime      string
		Interval    int
		Schedule    string
		Timestamp   string
		Targets     []TargetInfo
	}{
		TargetCount: len(pm.config.Targets),
		Uptime:      formatDuration(uptime),
		Interval:    pm.config.PingIntervalSeconds,
		Schedule:    schedule,
		Timestamp:   pm.getReportTime().Format("2006-01-02 15:04:05"),
		Targets:     targets,
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
		Name          string
		Address       string
		Label         string
		IsDown        bool
		IsSlow        bool
		HasPacketLoss bool
		LatencyMs     float64 // Latest ping latency in ms
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	for i, target := range pm.config.Targets {
		targets[i] = TargetInfo{
			Name:          target.Name,
			Address:       target.TargetAddr,
			Label:         getTargetLabel(target.TargetAddr),
			IsDown:        pm.downTargets[target.TargetAddr],
			IsSlow:        pm.slowTargets[target.TargetAddr],
			HasPacketLoss: pm.packetLossTargets[target.TargetAddr],
			LatencyMs:     pm.lastLatency[target.TargetAddr],
		}
	}
	pm.mu.RUnlock()
	
	data := struct {
		DownCount        int
		SlowCount        int
		PacketLossCount  int
		TotalTargets     int
		Timestamp        string
		LogCount         int
		Logs             []FormattedLog
		DownClass        string
		SlowClass        string
		PacketLossClass  string
		Targets          []TargetInfo
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
	}
	
	if err := pm.templates.ExecuteTemplate(w, "report_now.html", data); err != nil {
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
		Name          string
		Address       string
		Label         string
		IsDown        bool
		IsSlow        bool
		HasPacketLoss bool
		LatencyMs     float64 // Latest ping latency in ms
	}
	
	pm.mu.RLock()
	targets := make([]TargetInfo, len(pm.config.Targets))
	for i, target := range pm.config.Targets {
		targets[i] = TargetInfo{
			Name:          target.Name,
			Address:       target.TargetAddr,
			Label:         getTargetLabel(target.TargetAddr),
			IsDown:        pm.downTargets[target.TargetAddr],
			IsSlow:        pm.slowTargets[target.TargetAddr],
			HasPacketLoss: pm.packetLossTargets[target.TargetAddr],
			LatencyMs:     pm.lastLatency[target.TargetAddr],
		}
	}
	pm.mu.RUnlock()
	
	data := struct {
		DownCount        int
		SlowCount        int
		PacketLossCount  int
		TotalTargets     int
		Timestamp        string
		LogCount         int
		Logs             []FormattedLog
		DownClass        string
		SlowClass        string
		PacketLossClass  string
		EmailReport      string
		Schedule         string
		AllReports       []ReportWithContent
		ReportsDir       string
		Targets          []TargetInfo
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
	}
	
	if err := pm.templates.ExecuteTemplate(w, "report_all.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("⚠️  Template error: %v", err)
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
	
	// Protected routes (require auth if enabled, with security headers)
	http.HandleFunc("/", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleRoot))))
	http.HandleFunc("/reports", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReports))))
	http.HandleFunc("/report_now", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReportNow))))
	http.HandleFunc("/report_all", securityHeadersMiddleware(pm.rateLimitMiddleware(pm.AuthMiddleware(pm.handleReportAll))))
	
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
			log.Printf("❌ HTTP server error: %v", err)
			pm.addLog(fmt.Sprintf("HTTP server error: %v", err))
		}
	}()
}
