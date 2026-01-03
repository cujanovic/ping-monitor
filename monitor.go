package main

import (
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	brevo "github.com/getbrevo/brevo-go/lib"
)

// PingMonitor handles the monitoring logic
type PingMonitor struct {
	config                    Config
	downTargets               map[string]bool
	downSince                 map[string]time.Time
	slowTargets               map[string]bool
	slowSince                 map[string]time.Time
	slowConsecutiveCount      map[string]int         // Track consecutive high latency occurrences
	packetLossTargets         map[string]bool
	packetLossSince           map[string]time.Time
	packetLossConsecutiveCount map[string]int        // Track consecutive packet loss occurrences
	recoveryConsecutiveCount   map[string]int        // Track consecutive normal pings for recovery confirmation
	recoveryStartedAt          map[string]time.Time  // When first successful ping during recovery was received
	lastAlertTime             map[AlertKey]time.Time
	emailsSentThisHour        []time.Time // Global rolling window
	emailsSentPerTarget       map[string][]time.Time // Per-target rolling window
	emailsSentPerAlertType    map[string][]time.Time // Per-alert-type rolling window
	targetStats        map[string]*TargetStats
	statsStartTime     time.Time // Reset after each report for tracking report duration
	serviceStartTime   time.Time // Never reset - actual service start time
	lastLatency        map[string]float64          // Latest ping latency in ms
	latencyHistory     map[string][]LatencyPoint   // Historical latency data for graphs
	lastEmailReport    string
	lastEmailReportMu  sync.RWMutex
	httpRateLimiter    *HTTPRateLimiter
	sessionManager     *SessionManager
	templates          *template.Template
	brevoClient        *brevo.APIClient
	dnsCache           *DNSCache        // DNS resolution cache
	asyncLogger        *AsyncLogger     // Async logging system
	workerPool         *WorkerPool      // Worker pool for concurrent operations
	statsCache         *StatsCache      // Cached statistics for HTTP
	incidentsBuffer    *CircularBuffer  // Circular buffer for recent incidents
	targetLocks        map[string]*TargetLock // Per-target locks
	lastStateSave      time.Time        // Last time state was saved to disk
	stateSavePending   bool             // Flag indicating state needs to be saved
	criticalSavePending bool            // Flag for critical events that need immediate save
	disabledTargets    map[string]bool // Dynamically disabled targets (key: target address)
	stopChan           chan struct{}    // Signal to stop all goroutines
	mu                 sync.RWMutex
	emailMu            sync.Mutex
}

// NewPingMonitor creates a new PingMonitor instance
func NewPingMonitor(config Config) *PingMonitor {
	// Initialize Brevo client
	cfg := brevo.NewConfiguration()
	cfg.AddDefaultHeader("api-key", config.Email.APIKey)
	brevoClient := brevo.NewAPIClient(cfg)

	// Set defaults
	if config.PacketLossThresholdPercent == 0 {
		config.PacketLossThresholdPercent = 50
	}
	if config.HighLatencyConsecutiveCount == 0 {
		config.HighLatencyConsecutiveCount = 1 // Default: alert immediately (backward compatible)
	}
	if config.PacketLossConsecutiveCount == 0 {
		config.PacketLossConsecutiveCount = 1 // Default: alert immediately (backward compatible)
	}
	// Rapid confirmation defaults
	if config.HighLatencyRapidConfirmDelaySeconds == 0 {
		config.HighLatencyRapidConfirmDelaySeconds = 2
	}
	if config.HighLatencyRapidConfirmCount == 0 {
		config.HighLatencyRapidConfirmCount = 2
	}
	if config.HighLatencyRapidConfirmIntervalSeconds == 0 {
		config.HighLatencyRapidConfirmIntervalSeconds = 3
	}
	if config.PacketLossRapidConfirmDelaySeconds == 0 {
		config.PacketLossRapidConfirmDelaySeconds = 2
	}
	if config.PacketLossRapidConfirmCount == 0 {
		config.PacketLossRapidConfirmCount = 2
	}
	if config.PacketLossRapidConfirmIntervalSeconds == 0 {
		config.PacketLossRapidConfirmIntervalSeconds = 3
	}
	if config.AlertStatePingIntervalSeconds == 0 {
		config.AlertStatePingIntervalSeconds = 5 // Faster polling while in alert state
	}
	if config.RecoveryConfirmationCount == 0 {
		config.RecoveryConfirmationCount = 2 // Require 2 consecutive normal pings for recovery
	}
	if config.AlertCooldownMinutes == 0 {
		config.AlertCooldownMinutes = 15
	}
	if config.EmailRateLimitPerHour == 0 {
		config.EmailRateLimitPerHour = 60
	}
	if config.EmailCriticalReservePercent == 0 {
		config.EmailCriticalReservePercent = 30 // Reserve 30% for critical alerts by default
	}
	if config.EmailCriticalReservePercent > 50 {
		config.EmailCriticalReservePercent = 50 // Cap at 50%
	}
	if config.EmailPerAlertTypeLimits == nil {
		config.EmailPerAlertTypeLimits = make(map[string]int)
	}
	if config.MaxConcurrentPings == 0 {
		config.MaxConcurrentPings = 10
	}
	if config.DefaultTimeoutSeconds == 0 {
		config.DefaultTimeoutSeconds = 10
	}

	// Initialize target stats
	targetStats := make(map[string]*TargetStats)
	for _, target := range config.Targets {
		targetStats[target.TargetAddr] = &TargetStats{
			MinLatency:   -1,
			RecentEvents: make([]EventRecord, 0),
		}
	}

	// Set default HTTP log lines
	if config.HTTPLogLines == 0 {
		config.HTTPLogLines = 20
	}

	// Set default reports keep count
	if config.ReportsKeepCount == 0 {
		config.ReportsKeepCount = 10
	}

	// Set default log buffer flush interval
	if config.LogBufferFlushSeconds == 0 {
		config.LogBufferFlushSeconds = 5
	}

	// Set default recent incidents hours
	if config.RecentIncidentsHours == 0 {
		config.RecentIncidentsHours = 24
	}

	// Set default DNS cache TTL
	if config.DNSCacheTTLMinutes == 0 {
		config.DNSCacheTTLMinutes = 5 // Default: 5 minutes
	}
	
	// Set default recent events buffer size
	if config.RecentEventsBufferSize == 0 {
		config.RecentEventsBufferSize = 500 // Default: 500 events (24h+ of high-frequency incidents)
	}

	// Initialize DNS cache
	dnsCacheTTL := time.Duration(config.DNSCacheTTLMinutes) * time.Minute
	dnsCache := NewDNSCache(dnsCacheTTL)
	log.Printf("🗃️  DNS cache initialized with %d minute TTL", config.DNSCacheTTLMinutes)

	// Create reports directory if specified
	if config.ReportsDirectory != "" {
		if err := os.MkdirAll(config.ReportsDirectory, 0755); err != nil {
			log.Printf("⚠️  Failed to create reports directory: %v", err)
		}
	}

	// Set auth defaults
	if config.Argon2Memory == 0 {
		config.Argon2Memory = 65536 // 64 MB
	}
	if config.Argon2Time == 0 {
		config.Argon2Time = 3
	}
	if config.Argon2Threads == 0 {
		config.Argon2Threads = 4
	}
	if config.SessionTimeoutMinutes == 0 {
		config.SessionTimeoutMinutes = 60
	}
	if config.MaxLoginAttempts == 0 {
		config.MaxLoginAttempts = 5
	}
	if config.LockoutDurationMinutes == 0 {
		config.LockoutDurationMinutes = 15
	}

	// Initialize HTTP rate limiter
	var rateLimiter *HTTPRateLimiter
	if config.HTTPRateLimitPerMinute > 0 {
		rateLimiter = &HTTPRateLimiter{
			requests: make(map[string][]time.Time),
			limit:    config.HTTPRateLimitPerMinute,
			window:   time.Minute,
		}
	}

	// Initialize session manager
	sessionManager := NewSessionManager(&config)

	// Initialize HTML templates
	templates := initTemplates()

	// Initialize async logger
	flushInterval := time.Duration(config.LogBufferFlushSeconds) * time.Second
	asyncLogger := NewAsyncLogger(config.HTTPLogLines, flushInterval)

	// Initialize worker pool (use max_concurrent_pings as worker count)
	workerPool := NewWorkerPool(config.MaxConcurrentPings)

	// Initialize stats cache
	statsCache := NewStatsCache()

	// Initialize circular buffer for recent incidents (capacity = incidents per hour * hours)
	incidentsCapacity := 100 // Reasonable default
	incidentsBuffer := NewCircularBuffer(incidentsCapacity)

	// Initialize per-target locks
	targetLocks := make(map[string]*TargetLock)
	for _, target := range config.Targets {
		targetLocks[target.TargetAddr] = &TargetLock{}
	}

	pm := &PingMonitor{
		config:             config,
		downTargets:               make(map[string]bool),
		downSince:                 make(map[string]time.Time),
		slowTargets:               make(map[string]bool),
		slowSince:                 make(map[string]time.Time),
		slowConsecutiveCount:      make(map[string]int),
		packetLossTargets:         make(map[string]bool),
		packetLossSince:           make(map[string]time.Time),
		packetLossConsecutiveCount: make(map[string]int),
		recoveryConsecutiveCount:   make(map[string]int),
		recoveryStartedAt:          make(map[string]time.Time),
		lastAlertTime:             make(map[AlertKey]time.Time),
		emailsSentThisHour:     make([]time.Time, 0),
		emailsSentPerTarget:    make(map[string][]time.Time),
		emailsSentPerAlertType: make(map[string][]time.Time),
		targetStats:        targetStats,
		statsStartTime:     time.Now(),
		serviceStartTime:   time.Now(),
		lastLatency:        make(map[string]float64),
		latencyHistory:     make(map[string][]LatencyPoint),
		httpRateLimiter:    rateLimiter,
		sessionManager:     sessionManager,
		templates:          templates,
		brevoClient:        brevoClient,
		dnsCache:           dnsCache,
		asyncLogger:        asyncLogger,
		stopChan:           make(chan struct{}),
		workerPool:         workerPool,
		statsCache:         statsCache,
		incidentsBuffer:    incidentsBuffer,
		targetLocks:        targetLocks,
		disabledTargets:    make(map[string]bool),
	}

	return pm
}

// Stop gracefully stops all goroutines and saves state
func (pm *PingMonitor) Stop() {
	log.Printf("🛑 Stopping PingMonitor...")
	
	// Signal all goroutines to stop
	select {
	case <-pm.stopChan:
		// Already stopped
		return
	default:
		close(pm.stopChan)
	}
	
	// Stop components
	if pm.sessionManager != nil {
		pm.sessionManager.Stop()
	}
	if pm.asyncLogger != nil {
		pm.asyncLogger.Stop()
	}
	if pm.workerPool != nil {
		pm.workerPool.Stop()
	}
	
	// Save final state
	if pm.config.StateFilePath != "" {
		log.Printf("💾 Saving final state...")
		if err := pm.saveState(); err != nil {
			log.Printf("⚠️ Failed to save final state: %v", err)
		}
	}
	
	log.Printf("✅ PingMonitor stopped gracefully")
}

// Start begins the monitoring process
func (pm *PingMonitor) Start() {
	numTargets := len(pm.config.Targets)
	if numTargets == 0 {
		log.Fatal("No targets configured")
		return
	}

	log.Printf("🚀 Starting Ping Monitor with the following settings:")
	pm.addLog("🚀 Starting Ping Monitor")

	pm.logStartupInfo()

	// Start summary report scheduler if enabled
	if pm.config.SummaryReportEnabled {
		pm.startSummaryReportScheduler()
	}

	// Start stats cleanup scheduler (runs every hour to clean up old data)
	pm.startStatsCleanupScheduler()

	// Start HTTP server if enabled
	if pm.config.HTTPEnabled {
		msg := "   • HTTP Server: " + pm.config.HTTPListen
		log.Printf(msg)
		pm.addLog(msg)
		pm.startHTTPServer()
	}

	// Start DNS cache cleanup goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Minute) // Cleanup every 30 minutes
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pm.dnsCache.CleanupExpired()
			case <-pm.stopChan:
				return
			}
		}
	}()

	// Load previous report if available
	if pm.config.ReportsDirectory != "" {
		msg := "   • Reports Directory: " + pm.config.ReportsDirectory
		log.Printf(msg)
		pm.addLog(msg)
		pm.loadLatestReport()
	}

	// Load previous state (incidents) if available
	if pm.config.StateFilePath != "" {
		if err := pm.loadState(); err != nil {
			log.Printf("⚠️ Failed to load state: %v", err)
			pm.addLog(fmt.Sprintf("Failed to load state: %v", err))
		}
	}

	// Start event-driven state saver goroutine with throttling
	// Only saves when incidents occur, but throttles to avoid excessive writes
	// Critical events (DOWN state changes) are saved immediately
	if pm.config.StateFilePath != "" {
		throttleSeconds := pm.config.StateSaveIntervalSeconds
		if throttleSeconds <= 0 {
			throttleSeconds = 5 // Default: max 1 save per 5 seconds
		}
		
		go func() {
			ticker := time.NewTicker(1 * time.Second) // Check every second
			defer ticker.Stop()
			
			for {
				select {
				case <-ticker.C:
					pm.mu.Lock()
					needsSave := pm.stateSavePending
					criticalSave := pm.criticalSavePending
					
					// Critical events bypass throttle
					if criticalSave {
						pm.stateSavePending = false
						pm.criticalSavePending = false
						pm.lastStateSave = time.Now()
						pm.mu.Unlock()
						
						if err := pm.saveState(); err != nil {
							log.Printf("⚠️ Failed to save critical state: %v", err)
						} else {
							log.Printf("💾 Critical state saved (alert state change)")
						}
						continue
					}
					
					// Check throttle: only save if enough time has passed since last save
					if needsSave {
						timeSinceLastSave := time.Since(pm.lastStateSave).Seconds()
						if timeSinceLastSave >= float64(throttleSeconds) {
							pm.stateSavePending = false
							pm.lastStateSave = time.Now()
							pm.mu.Unlock()
							
							// Save state (outside lock to avoid blocking)
							if err := pm.saveState(); err != nil {
								log.Printf("⚠️ Failed to save state: %v", err)
							}
						} else {
							pm.mu.Unlock()
						}
					} else {
						pm.mu.Unlock()
					}
				
				case <-pm.stopChan:
					return
				}
			}
		}()
		
		log.Printf("💾 State persistence enabled: %s (event-driven, throttle: %ds, critical events immediate)", 
			pm.config.StateFilePath, throttleSeconds)
		pm.addLog(fmt.Sprintf("State persistence enabled (event-driven): %s", pm.config.StateFilePath))
	}

	// Pre-resolve DNS targets in parallel for faster first cycle
	pm.preResolveDNSTargets()

	// Shuffle targets for randomized monitoring
	targets := make([]Target, len(pm.config.Targets))
	copy(targets, pm.config.Targets)
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(targets), func(i, j int) {
		targets[i], targets[j] = targets[j], targets[i]
	})
	log.Printf("🔀 Targets shuffled for randomized monitoring order")
	pm.addLog("🔀 Targets shuffled for randomized monitoring order")

	// Calculate delay between checks using configurable stagger window
	intervalSeconds := time.Duration(pm.config.PingIntervalSeconds) * time.Second
	
	// Use stagger window if configured, otherwise fall back to old behavior
	var delayBetweenTargets time.Duration
	if pm.config.StaggerWindowSeconds > 0 {
		staggerWindow := time.Duration(pm.config.StaggerWindowSeconds) * time.Second
		delayBetweenTargets = staggerWindow / time.Duration(numTargets)
		log.Printf("📊 Distributing pings over %v window (%v delay between targets)", 
			staggerWindow, delayBetweenTargets)
		pm.addLog(fmt.Sprintf("📊 Distributing pings over %ds window", pm.config.StaggerWindowSeconds))
	} else {
		// Fallback: spread evenly across entire interval
		delayBetweenTargets = intervalSeconds / time.Duration(numTargets)
		log.Printf("📊 Distributing pings across full interval (%v delay between targets)", delayBetweenTargets)
		pm.addLog("📊 Distributing pings across full interval")
	}

	// Start monitoring goroutines
	for i, target := range targets {
		initialDelay := time.Duration(i) * delayBetweenTargets

		go func(t Target, delay time.Duration) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🆘 Monitoring goroutine for %s panicked: %v", formatTargetInfo(t), r)
					// Restart the goroutine
					go pm.startMonitoringLoop(t, delay, intervalSeconds)
				}
			}()

			pm.startMonitoringLoop(t, delay, intervalSeconds)
		}(target, initialDelay)
	}

	log.Printf("✅ All monitoring goroutines started")
	pm.addLog("✅ All monitoring goroutines started")

	// Keep main goroutine running
	select {}
}

// startMonitoringLoop runs the monitoring loop for a single target
func (pm *PingMonitor) startMonitoringLoop(t Target, delay time.Duration, interval time.Duration) {
	time.Sleep(delay)
	
	// Check if target is disabled before initial ping
	pm.mu.RLock()
	isDisabled := pm.disabledTargets[t.TargetAddr]
	pm.mu.RUnlock()
	
	if !isDisabled {
		// Cache time.Now() once per cycle and pass it down
		now := time.Now()
		pm.monitorTarget(t, now, true) // Initial ping counts in stats
	}

	alertInterval := time.Duration(pm.config.AlertStatePingIntervalSeconds) * time.Second
	wasInAlertState := false

	for {
		// Check if target is in alert state
		pm.mu.RLock()
		inAlertState := pm.downTargets[t.TargetAddr] || pm.slowTargets[t.TargetAddr] || pm.packetLossTargets[t.TargetAddr]
		pm.mu.RUnlock()

		// Determine which interval to use
		var waitDuration time.Duration
		var updateStats bool
		if inAlertState && alertInterval < interval {
			waitDuration = alertInterval
			updateStats = false // Don't count rapid polling pings in stats
			if !wasInAlertState {
				log.Printf("⚡ %s entering rapid polling mode (%ds interval, stats paused)", t.Name, pm.config.AlertStatePingIntervalSeconds)
			}
		} else {
			waitDuration = interval
			updateStats = true // Normal interval pings count in stats
			if wasInAlertState {
				log.Printf("⏱️  %s returning to normal polling mode (%ds interval)", t.Name, pm.config.PingIntervalSeconds)
			}
		}
		wasInAlertState = inAlertState

		// Wait for the interval or stop signal
		select {
		case <-time.After(waitDuration):
			// Check if target is disabled before monitoring
			pm.mu.RLock()
			isDisabled := pm.disabledTargets[t.TargetAddr]
			pm.mu.RUnlock()
			
			if isDisabled {
				// Target is disabled, skip monitoring but continue loop
				continue
			}
			
			now := time.Now()
			pm.monitorTarget(t, now, updateStats)
		case <-pm.stopChan:
			return
		}
	}
}

// logStartupInfo logs startup configuration information
func (pm *PingMonitor) logStartupInfo() {
	log.Printf("   • Targets: %d", len(pm.config.Targets))
	pm.addLog(fmt.Sprintf("   • Targets: %d", len(pm.config.Targets)))

	log.Printf("   • Ping Interval: %d seconds", pm.config.PingIntervalSeconds)
	log.Printf("   • Ping Count: %d", pm.config.PingCount)
	log.Printf("   • Default Timeout: %d seconds", pm.config.DefaultTimeoutSeconds)
	log.Printf("   • Packet Loss Threshold: %d%%", pm.config.PacketLossThresholdPercent)
	log.Printf("   • Alert Cooldown: %d minutes", pm.config.AlertCooldownMinutes)
	log.Printf("   • Email Rate Limit: %d/hour (per-target: %d, critical reserve: %d%%)", 
		pm.config.EmailRateLimitPerHour, 
		pm.config.EmailRateLimitPerTargetPerHour,
		pm.config.EmailCriticalReservePercent)
	log.Printf("   • Max Concurrent Pings: %d", pm.config.MaxConcurrentPings)
	log.Printf("   • Alert State Ping Interval: %d seconds", pm.config.AlertStatePingIntervalSeconds)
	log.Printf("   • Recovery Confirmation: %d consecutive checks", pm.config.RecoveryConfirmationCount)
	
	if pm.config.UseRawSockets {
		log.Printf("   • Raw Sockets: enabled (requires CAP_NET_RAW)")
	} else {
		log.Printf("   • Raw Sockets: disabled (unprivileged mode)")
	}

	if pm.config.SummaryReportEnabled {
		msg := "   • Summary Reports: " + pm.config.SummaryReportSchedule + " at " + pm.config.SummaryReportTime
		log.Printf(msg)
		pm.addLog(msg)
	}
}

// getTargetTimeout returns the effective timeout for a target
func (pm *PingMonitor) getTargetTimeout(target Target) time.Duration {
	if target.TimeoutSeconds > 0 {
		return time.Duration(target.TimeoutSeconds) * time.Second
	}
	return time.Duration(pm.config.DefaultTimeoutSeconds) * time.Second
}

// getTargetThreshold returns the effective ping threshold for a target
func (pm *PingMonitor) getTargetThreshold(target Target) int {
	if target.PingThresholdMs > 0 {
		return target.PingThresholdMs
	}
	if pm.config.PingTimeThresholdMs > 0 {
		return pm.config.PingTimeThresholdMs
	}
	return 200
}

// getPacketLossThreshold returns the effective packet loss threshold for a target
func (pm *PingMonitor) getPacketLossThreshold(target Target) int {
	if target.PacketLossThresholdPercent > 0 {
		return target.PacketLossThresholdPercent
	}
	if pm.config.PacketLossThresholdPercent > 0 {
		return pm.config.PacketLossThresholdPercent
	}
	return 50
}

// getReportTime returns the current time adjusted by the configured offset
func (pm *PingMonitor) getReportTime() time.Time {
	return time.Now().Add(time.Duration(pm.config.ReportTimeOffsetHours) * time.Hour)
}

// preResolveDNSTargets pre-resolves all DNS targets in parallel on startup
func (pm *PingMonitor) preResolveDNSTargets() {
	dnsTargets := make([]Target, 0)
	
	// Identify DNS targets (not IPs)
	for _, target := range pm.config.Targets {
		if net.ParseIP(target.TargetAddr) == nil {
			dnsTargets = append(dnsTargets, target)
		}
	}
	
	if len(dnsTargets) == 0 {
		return // No DNS targets to resolve
	}
	
	log.Printf("🔍 Pre-resolving %d DNS targets in parallel...", len(dnsTargets))
	
	var wg sync.WaitGroup
	startTime := time.Now()
	
	for _, target := range dnsTargets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			resolvedIP, _, err := pm.dnsCache.Resolve(t.TargetAddr)
			if err != nil {
				log.Printf("⚠️  Pre-resolution failed for %s: %v", t.Name, err)
			} else {
				log.Printf("✓ Pre-resolved %s → %s", t.Name, resolvedIP)
			}
		}(target)
	}
	
	wg.Wait()
	duration := time.Since(startTime)
	log.Printf("✅ Pre-resolved %d DNS targets in %v", len(dnsTargets), duration)
	pm.addLog(fmt.Sprintf("Pre-resolved %d DNS targets in %v", len(dnsTargets), duration))
}

