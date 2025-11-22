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
	LastSaved      time.Time                `json:"last_saved"`
	Events         map[string][]EventRecord `json:"events"`          // key: target address
	TargetStats    map[string]*TargetStats  `json:"target_stats"`    // key: target address
	StatsStartTime time.Time                `json:"stats_start_time"` // When stats collection started
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
	}

	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	
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

	log.Printf("💾 Restored %d events and %d target stats from state file (saved: %s)", 
		restoredEventsCount, restoredStatsCount, state.LastSaved.Format("2006-01-02 15:04:05"))

	return nil
}

// cleanupOldStats removes statistics data that falls outside the configured time window
func (pm *PingMonitor) cleanupOldStats() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	
	// If statsStartTime is too old, we need to reset stats because all data is invalid
	if pm.statsStartTime.Before(cutoffTime) {
		log.Printf("⚠️  Stats data older than %d hours detected, resetting counters", pm.config.RecentIncidentsHours)
		pm.statsStartTime = cutoffTime
		
		// Reset all statistics counters since they contain data outside the window
		// We keep RecentEvents as they'll be cleaned below
		for _, stats := range pm.targetStats {
			if stats == nil {
				continue
			}
			
			// Keep events, but reset counters
			oldEvents := stats.RecentEvents
			stats.TotalChecks = 0
			stats.SuccessfulChecks = 0
			stats.FailedChecks = 0
			stats.TotalLatency = 0
			stats.MinLatency = -1
			stats.MaxLatency = 0
			stats.TotalPacketLoss = 0
			stats.MaxPacketLoss = 0
			stats.HighLatencyCount = 0
			stats.PacketLossEvents = 0
			stats.RecentEvents = oldEvents // Keep for cleaning below
		}
		
		log.Printf("🧹 Stats counters reset, start time adjusted to: %s", pm.statsStartTime.Format("2006-01-02 15:04:05"))
	}
	
	// Clean up old events
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
