package main

import (
	"sync"
	"time"
)

// EventRecord tracks a single event occurrence
type EventRecord struct {
	Timestamp time.Time
	EventType string  // "down", "up", "high_latency", "latency_normal", "packet_loss", "packet_loss_normal"
	Value     float64 // latency in ms or packet loss percentage
	Threshold float64 // threshold value
	Duration  time.Duration // for recovery events - how long the issue lasted
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

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Message   string
}

// HTTPRateLimiter tracks HTTP requests per IP
type HTTPRateLimiter struct {
	requests map[string][]time.Time
	mu       sync.Mutex
	limit    int
	window   time.Duration
}

// TargetReport holds computed statistics for a target
type TargetReport struct {
	Target        Target
	Uptime        float64
	AvgLatency    float64
	MinLatency    float64
	MaxLatency    float64
	AvgPacketLoss float64
	TotalIssues   int64
	Stats         *TargetStats
}

// ReportWithContent holds a report filename and its content
type ReportWithContent struct {
	Filename string
	Content  string
}

