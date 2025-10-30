package main

import (
	_ "sync" // Used in types.go
	"time"
)

// NewAsyncLogger creates a new async logger
func NewAsyncLogger(maxLines int, flushInterval time.Duration) *AsyncLogger {
	logger := &AsyncLogger{
		logChan:     make(chan LogEntry, 1000), // Buffered channel
		buffer:      make([]LogEntry, 0, maxLines),
		maxLines:    maxLines,
		flushTicker: time.NewTicker(flushInterval),
		stopChan:    make(chan struct{}),
	}
	
	// Start async log processor
	go logger.processLogs()
	
	return logger
}

// Log sends a log entry asynchronously
func (al *AsyncLogger) Log(message string) {
	select {
	case al.logChan <- LogEntry{Timestamp: time.Now(), Message: message}:
		// Sent successfully
	default:
		// Channel full, drop log (prevents blocking)
	}
}

// processLogs runs in a goroutine to process log entries
func (al *AsyncLogger) processLogs() {
	for {
		select {
		case entry := <-al.logChan:
			al.mu.Lock()
			al.buffer = append(al.buffer, entry)
			// Keep only last N entries
			if len(al.buffer) > al.maxLines {
				al.buffer = al.buffer[len(al.buffer)-al.maxLines:]
			}
			al.mu.Unlock()
			
		case <-al.flushTicker.C:
			// Periodic flush (if needed)
			al.flush()
			
		case <-al.stopChan:
			// Drain remaining logs
			al.drain()
			return
		}
	}
}

// flush performs periodic maintenance
func (al *AsyncLogger) flush() {
	al.mu.Lock()
	defer al.mu.Unlock()
	
	// Trim buffer if needed
	if len(al.buffer) > al.maxLines {
		al.buffer = al.buffer[len(al.buffer)-al.maxLines:]
	}
}

// drain processes remaining logs before shutdown
func (al *AsyncLogger) drain() {
	for {
		select {
		case entry := <-al.logChan:
			al.mu.Lock()
			al.buffer = append(al.buffer, entry)
			if len(al.buffer) > al.maxLines {
				al.buffer = al.buffer[len(al.buffer)-al.maxLines:]
			}
			al.mu.Unlock()
		default:
			return
		}
	}
}

// GetLogs returns a copy of current log buffer
func (al *AsyncLogger) GetLogs() []LogEntry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	
	// Return copy to avoid race conditions
	logs := make([]LogEntry, len(al.buffer))
	copy(logs, al.buffer)
	return logs
}

// Stop gracefully stops the async logger
func (al *AsyncLogger) Stop() {
	close(al.stopChan)
	al.flushTicker.Stop()
}

