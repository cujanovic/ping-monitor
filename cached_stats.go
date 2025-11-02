package main

import (
	"sync"
	"time"
)

// NewCachedStats creates a new cached stats holder
func NewCachedStats() *CachedStats {
	return &CachedStats{
		timestamp: time.Time{}, // Zero time means not initialized
	}
}

// Get returns cached data if still valid
func (cs *CachedStats) Get(maxAge time.Duration) (interface{}, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	
	if cs.data == nil {
		return nil, false
	}
	
	age := time.Since(cs.timestamp)
	if age > maxAge {
		return nil, false // Cache expired
	}
	
	return cs.data, true
}

// Set updates the cached data
func (cs *CachedStats) Set(data interface{}) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	
	cs.data = data
	cs.timestamp = time.Now()
}

// Invalidate clears the cache
func (cs *CachedStats) Invalidate() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	
	cs.data = nil
	cs.timestamp = time.Time{}
}

// Age returns how old the cached data is
func (cs *CachedStats) Age() time.Duration {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	
	if cs.timestamp.IsZero() {
		return time.Duration(0)
	}
	
	return time.Since(cs.timestamp)
}

// StatsCache manages multiple cached statistics
type StatsCache struct {
	caches map[string]*CachedStats
	mu     sync.RWMutex
}

// NewStatsCache creates a new stats cache manager
func NewStatsCache() *StatsCache {
	return &StatsCache{
		caches: make(map[string]*CachedStats),
	}
}

// GetOrCompute gets cached value or computes it
func (sc *StatsCache) GetOrCompute(key string, maxAge time.Duration, computeFn func() interface{}) interface{} {
	sc.mu.RLock()
	cache, exists := sc.caches[key]
	sc.mu.RUnlock()
	
	if !exists {
		sc.mu.Lock()
		cache = NewCachedStats()
		sc.caches[key] = cache
		sc.mu.Unlock()
	}
	
	// Try to get from cache
	if data, valid := cache.Get(maxAge); valid {
		return data
	}
	
	// Compute new value
	data := computeFn()
	cache.Set(data)
	
	return data
}

// Invalidate invalidates a specific cache
func (sc *StatsCache) Invalidate(key string) {
	sc.mu.RLock()
	cache, exists := sc.caches[key]
	sc.mu.RUnlock()
	
	if exists {
		cache.Invalidate()
	}
}

// InvalidateAll invalidates all caches
func (sc *StatsCache) InvalidateAll() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	
	for _, cache := range sc.caches {
		cache.Invalidate()
	}
}




