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
	config             Config
	downTargets        map[string]bool
	downSince          map[string]time.Time
	slowTargets        map[string]bool
	slowSince          map[string]time.Time
	packetLossTargets  map[string]bool
	packetLossSince    map[string]time.Time
	lastAlertTime      map[AlertKey]time.Time
	emailsSentThisHour []time.Time
	targetStats        map[string]*TargetStats
	statsStartTime     time.Time // Reset after each report for tracking report duration
	serviceStartTime   time.Time // Never reset - actual service start time
	lastLatency        map[string]float64 // Latest ping latency in ms
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
	if config.AlertCooldownMinutes == 0 {
		config.AlertCooldownMinutes = 15
	}
	if config.EmailRateLimitPerHour == 0 {
		config.EmailRateLimitPerHour = 60
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
		serviceStartTime:   time.Now(),
		lastLatency:        make(map[string]float64),
		httpRateLimiter:    rateLimiter,
		sessionManager:     sessionManager,
		templates:          templates,
		brevoClient:        brevoClient,
		dnsCache:           dnsCache,
		asyncLogger:        asyncLogger,
		workerPool:         workerPool,
		statsCache:         statsCache,
		incidentsBuffer:    incidentsBuffer,
		targetLocks:        targetLocks,
	}

	return pm
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
		for range ticker.C {
			pm.dnsCache.CleanupExpired()
		}
	}()

	// Load previous report if available
	if pm.config.ReportsDirectory != "" {
		msg := "   • Reports Directory: " + pm.config.ReportsDirectory
		log.Printf(msg)
		pm.addLog(msg)
		pm.loadLatestReport()
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

	// Calculate delay between checks
	intervalSeconds := time.Duration(pm.config.PingIntervalSeconds) * time.Second
	delayBetweenTargets := intervalSeconds / time.Duration(numTargets)

	log.Printf("📊 Distributing pings with %v delay between targets", delayBetweenTargets)
	pm.addLog("📊 Distributing pings with delay between targets")

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
	// Cache time.Now() once per cycle and pass it down
	now := time.Now()
	pm.monitorTarget(t, now)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		// Cache time.Now() once per cycle and pass it down
		now := time.Now()
		pm.monitorTarget(t, now)
	}
}

// logStartupInfo logs startup configuration information
func (pm *PingMonitor) logStartupInfo() {
	log.Printf("   • Targets: %d", len(pm.config.Targets))
	pm.addLog("   • Targets: " + string(rune(len(pm.config.Targets))))

	log.Printf("   • Ping Interval: %d seconds", pm.config.PingIntervalSeconds)
	log.Printf("   • Ping Count: %d", pm.config.PingCount)
	log.Printf("   • Default Timeout: %d seconds", pm.config.DefaultTimeoutSeconds)
	log.Printf("   • Packet Loss Threshold: %d%%", pm.config.PacketLossThresholdPercent)
	log.Printf("   • Alert Cooldown: %d minutes", pm.config.AlertCooldownMinutes)
	log.Printf("   • Email Rate Limit: %d/hour", pm.config.EmailRateLimitPerHour)
	log.Printf("   • Max Concurrent Pings: %d", pm.config.MaxConcurrentPings)
	
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

