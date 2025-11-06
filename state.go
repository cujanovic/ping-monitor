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
	LastSaved time.Time         `json:"last_saved"`
	Events    map[string][]EventRecord `json:"events"` // key: target address
}

// saveState saves the current incidents to disk
func (pm *PingMonitor) saveState() error {
	if pm.config.StateFilePath == "" {
		return nil // State persistence disabled
	}

	pm.mu.RLock()
	
	// Collect all events from all targets
	state := StateFile{
		LastSaved: time.Now(),
		Events:    make(map[string][]EventRecord),
	}

	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	
	for addr, stats := range pm.targetStats {
		if stats == nil || len(stats.RecentEvents) == 0 {
			continue
		}
		
		// Filter events within the time window
		validEvents := make([]EventRecord, 0)
		for _, event := range stats.RecentEvents {
			if event.Timestamp.After(cutoffTime) {
				validEvents = append(validEvents, event)
			}
		}
		
		if len(validEvents) > 0 {
			state.Events[addr] = validEvents
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

	// Restore events
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cutoffTime := time.Now().Add(-time.Duration(pm.config.RecentIncidentsHours) * time.Hour)
	restoredCount := 0
	
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
				restoredCount++
			}
		}

		if len(validEvents) > 0 {
			stats.RecentEvents = validEvents
		}
	}

	log.Printf("💾 Restored %d events from state file (saved: %s)", 
		restoredCount, state.LastSaved.Format("2006-01-02 15:04:05"))

	return nil
}
