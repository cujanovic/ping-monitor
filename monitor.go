package main

import (
	"html/template"
	"log"
	"math/rand"
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
	statsStartTime     time.Time
	logBuffer          []LogEntry
	logPendingBuffer   []LogEntry
	logMu              sync.Mutex
	lastEmailReport    string
	lastEmailReportMu  sync.RWMutex
	httpRateLimiter    *HTTPRateLimiter
	templates          *template.Template
	brevoClient        *brevo.APIClient
	mu                 sync.RWMutex
	emailMu            sync.Mutex
	semaphore          chan struct{}
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

	// Create semaphore for concurrent ping limiting
	semaphore := make(chan struct{}, config.MaxConcurrentPings)

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

	// Create reports directory if specified
	if config.ReportsDirectory != "" {
		if err := os.MkdirAll(config.ReportsDirectory, 0755); err != nil {
			log.Printf("⚠️  Failed to create reports directory: %v", err)
		}
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

	// Initialize HTML templates
	templates := initTemplates()

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
		logBuffer:          make([]LogEntry, 0, config.HTTPLogLines),
		logPendingBuffer:   make([]LogEntry, 0),
		httpRateLimiter:    rateLimiter,
		templates:          templates,
		brevoClient:        brevoClient,
		semaphore:          semaphore,
	}

	// Start log buffer flusher
	go pm.logBufferFlusher()

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

	// Load previous report if available
	if pm.config.ReportsDirectory != "" {
		msg := "   • Reports Directory: " + pm.config.ReportsDirectory
		log.Printf(msg)
		pm.addLog(msg)
		pm.loadLatestReport()
	}

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
	pm.monitorTarget(t)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		pm.monitorTarget(t)
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

