package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// StateFile represents the persisted state
type StateFile struct {
	LastSaved      time.Time                  `json:"last_saved"`
	Events         map[string][]EventRecord   `json:"events"`           // key: target address
	TargetStats    map[string]*TargetStats    `json:"target_stats"`     // key: target address
	StatsStartTime time.Time                  `json:"stats_start_time"` // When stats collection started
	LatencyHistory map[string][]LatencyPoint  `json:"latency_history"`  // key: target address, for graphs
}

// saveState saves the current incidents to disk
func (pm *PingMonitor) saveState() error {
	if pm.config.StateFilePath == "" {
		return nil // State persistence disabled
	}

	pm.mu.RLock()
	
	// Collect all events and stats from all targets
	state := StateFile{
		LastSaved:      time.Now(),
		Events:         make(map[string][]EventRecord),
		TargetStats:    make(map[string]*TargetStats),
		StatsStartTime: pm.statsStartTime,
		LatencyHistory: make(map[string][]LatencyPoint),
	}

	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	latencyCutoff := time.Now().Add(-49 * 24 * time.Hour) // Keep 7 weeks of latency data
	
	for addr, stats := range pm.targetStats {
		if stats == nil {
			continue
		}
		
		// Make a deep copy of stats to avoid race conditions
		statsCopy := &TargetStats{
			TotalChecks:       stats.TotalChecks,
			SuccessfulChecks:  stats.SuccessfulChecks,
			FailedChecks:      stats.FailedChecks,
			TotalLatency:      stats.TotalLatency,
			MinLatency:        stats.MinLatency,
			MaxLatency:        stats.MaxLatency,
			TotalPacketLoss:   stats.TotalPacketLoss,
			MaxPacketLoss:     stats.MaxPacketLoss,
			HighLatencyCount:  stats.HighLatencyCount,
			PacketLossEvents:  stats.PacketLossEvents,
			RecentEvents:      nil, // Don't duplicate events here, saved separately
		}
		state.TargetStats[addr] = statsCopy
		
		// Filter events within the time window
		if len(stats.RecentEvents) > 0 {
			validEvents := make([]EventRecord, 0, len(stats.RecentEvents))
		for _, event := range stats.RecentEvents {
			if event.Timestamp.After(cutoffTime) {
				validEvents = append(validEvents, event)
			}
		}
		
		if len(validEvents) > 0 {
			state.Events[addr] = validEvents
			}
		}
	}
	
	// Save latency history (last 7 weeks)
	for addr, points := range pm.latencyHistory {
		validPoints := make([]LatencyPoint, 0, len(points))
		for _, point := range points {
			if point.Timestamp.After(latencyCutoff) {
				validPoints = append(validPoints, point)
			}
		}
		if len(validPoints) > 0 {
			state.LatencyHistory[addr] = validPoints
		}
	}
	
	pm.mu.RUnlock()

	// Marshal to JSON
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %v", err)
	}

	// Write to temporary file first
	tmpFile := pm.config.StateFilePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %v", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, pm.config.StateFilePath); err != nil {
		// Cleanup temp file on error
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename state file: %v", err)
	}

	return nil
}

// loadState loads previously saved incidents from disk
func (pm *PingMonitor) loadState() error {
	if pm.config.StateFilePath == "" {
		return nil // State persistence disabled
	}

	// Check if file exists
	if _, err := os.Stat(pm.config.StateFilePath); os.IsNotExist(err) {
		log.Printf("📝 No previous state file found, starting fresh")
		return nil
	}

	// Read state file
	data, err := os.ReadFile(pm.config.StateFilePath)
	if err != nil {
		return fmt.Errorf("failed to read state file: %v", err)
	}

	// Check if file is empty
	if len(data) == 0 {
		log.Printf("⚠️ State file is empty, starting fresh")
		return nil
	}

	var state StateFile
	if err := json.Unmarshal(data, &state); err != nil {
		// Log error but don't fail - corrupted state shouldn't prevent startup
		log.Printf("⚠️ Failed to parse state file (corrupted?): %v", err)
		log.Printf("   Starting with fresh state. Corrupted file will be overwritten.")
		// Optionally backup the corrupted file
		backupPath := pm.config.StateFilePath + ".corrupted"
		if err := os.Rename(pm.config.StateFilePath, backupPath); err == nil {
			log.Printf("   Corrupted file backed up to: %s", backupPath)
		}
		return nil
	}

	// Restore events and stats
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	restoredEventsCount := 0
	restoredStatsCount := 0
	
	// Restore stats start time if it's within the configured period
	if !state.StatsStartTime.IsZero() && state.StatsStartTime.After(cutoffTime) {
		pm.statsStartTime = state.StatsStartTime
		log.Printf("📊 Restored stats start time: %s", state.StatsStartTime.Format("2006-01-02 15:04:05"))
	} else {
		// Stats are too old or missing, start fresh
		pm.statsStartTime = time.Now()
		log.Printf("📊 Stats start time too old or missing, starting fresh: %s", pm.statsStartTime.Format("2006-01-02 15:04:05"))
	}
	
	// Restore target statistics
	for addr, savedStats := range state.TargetStats {
		stats, exists := pm.targetStats[addr]
		if !exists {
			// Target may have been removed from config, skip
			continue
		}
		
		if savedStats != nil {
			// Restore the statistics counters
			stats.TotalChecks = savedStats.TotalChecks
			stats.SuccessfulChecks = savedStats.SuccessfulChecks
			stats.FailedChecks = savedStats.FailedChecks
			stats.TotalLatency = savedStats.TotalLatency
			stats.MinLatency = savedStats.MinLatency
			stats.MaxLatency = savedStats.MaxLatency
			stats.TotalPacketLoss = savedStats.TotalPacketLoss
			stats.MaxPacketLoss = savedStats.MaxPacketLoss
			stats.HighLatencyCount = savedStats.HighLatencyCount
			stats.PacketLossEvents = savedStats.PacketLossEvents
			restoredStatsCount++
		}
	}
	
	// Restore events
	for addr, events := range state.Events {
		stats, exists := pm.targetStats[addr]
		if !exists {
			// Target may have been removed from config, skip
			continue
		}

		// Filter events within the time window
		validEvents := make([]EventRecord, 0)
		for _, event := range events {
			if event.Timestamp.After(cutoffTime) {
				validEvents = append(validEvents, event)
				restoredEventsCount++
			}
		}

		if len(validEvents) > 0 {
			stats.RecentEvents = validEvents
		}
	}

	// Restore latency history
	restoredLatencyPoints := 0
	latencyCutoff := time.Now().Add(-49 * 24 * time.Hour) // 7 weeks
	for addr, points := range state.LatencyHistory {
		// Only restore if target still exists
		if _, exists := pm.targetStats[addr]; !exists {
			continue
		}
		
		validPoints := make([]LatencyPoint, 0, len(points))
		for _, point := range points {
			if point.Timestamp.After(latencyCutoff) {
				validPoints = append(validPoints, point)
				restoredLatencyPoints++
			}
		}
		if len(validPoints) > 0 {
			pm.latencyHistory[addr] = validPoints
		}
	}

	log.Printf("💾 Restored %d events, %d target stats, and %d latency points from state file (saved: %s)", 
		restoredEventsCount, restoredStatsCount, restoredLatencyPoints, state.LastSaved.Format("2006-01-02 15:04:05"))

	return nil
}

// cleanupOldStats removes event data that falls outside the configured time window
// Note: Stats counters (TotalChecks, SuccessfulChecks, etc.) are cumulative since
// service start and are NOT time-windowed, as we cannot accurately recalculate them
// from events alone (events only record failures, not successes).
func (pm *PingMonitor) cleanupOldStats() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	
	// Clean up old events only - do NOT reset counters
	// Counters represent cumulative stats since service start
	cleanedTargets := 0
	for _, stats := range pm.targetStats {
		if stats == nil || len(stats.RecentEvents) == 0 {
			continue
		}
		
		originalCount := len(stats.RecentEvents)
		validEvents := make([]EventRecord, 0, len(stats.RecentEvents))
		for _, event := range stats.RecentEvents {
			if event.Timestamp.After(cutoffTime) {
				validEvents = append(validEvents, event)
			}
		}
		
		if len(validEvents) < originalCount {
			stats.RecentEvents = validEvents
			cleanedTargets++
		}
	}
	
	if cleanedTargets > 0 {
		log.Printf("🧹 Cleaned old events from %d targets", cleanedTargets)
	}
}

// startStatsCleanupScheduler periodically cleans up old statistics
func (pm *PingMonitor) startStatsCleanupScheduler() {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		
		for range ticker.C {
			pm.cleanupOldStats()
		}
	}()
}
