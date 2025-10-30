package main

import (
	"log"
	"net"
	"time"
)

// NewDNSCache creates a new DNS cache with specified TTL
func NewDNSCache(ttl time.Duration) *DNSCache {
	if ttl == 0 {
		ttl = 5 * time.Minute // Default: 5 minutes
	}
	return &DNSCache{
		entries: make(map[string]*DNSCacheEntry),
		ttl:     ttl,
	}
}

// Resolve resolves a target address, using cache if available and valid
// Returns the IP to ping and whether IP changed from last resolution
func (dc *DNSCache) Resolve(targetAddr string) (string, bool, error) {
	// Check if it's already an IP address
	if net.ParseIP(targetAddr) != nil {
		// It's an IP, no DNS needed
		return targetAddr, false, nil
	}

	dc.mu.RLock()
	entry, exists := dc.entries[targetAddr]
	dc.mu.RUnlock()

	now := time.Now()
	
	// If cache exists and hasn't expired, use it
	if exists && now.Before(entry.ExpiresAt) {
		entry.mu.RLock()
		cachedIP := entry.ResolvedIP
		entry.mu.RUnlock()
		return cachedIP, false, nil
	}

	// Need to resolve DNS
	ips, err := net.LookupIP(targetAddr)
	if err != nil {
		// If we have an expired cache entry, use it as fallback
		if exists {
			entry.mu.RLock()
			fallbackIP := entry.ResolvedIP
			entry.mu.RUnlock()
			log.Printf("⚠️  DNS lookup failed for %s, using cached IP %s: %v", targetAddr, fallbackIP, err)
			return fallbackIP, false, err
		}
		return "", false, err
	}

	if len(ips) == 0 {
		// No IPs resolved, use cache fallback if available
		if exists {
			entry.mu.RLock()
			fallbackIP := entry.ResolvedIP
			entry.mu.RUnlock()
			log.Printf("⚠️  No IPs resolved for %s, using cached IP %s", targetAddr, fallbackIP)
			return fallbackIP, false, nil
		}
		return "", false, &net.DNSError{Err: "no IP addresses found", Name: targetAddr, IsNotFound: true}
	}

	// Use first IPv4 address (prefer IPv4 for ICMP ping)
	var resolvedIP string
	for _, ip := range ips {
		if ip.To4() != nil {
			resolvedIP = ip.String()
			break
		}
	}
	
	// If no IPv4, use first IPv6
	if resolvedIP == "" {
		resolvedIP = ips[0].String()
	}

	// Check if IP changed
	ipChanged := false
	if exists {
		entry.mu.RLock()
		oldIP := entry.ResolvedIP
		entry.mu.RUnlock()
		
		if oldIP != resolvedIP {
			ipChanged = true
			log.Printf("🔄 DDNS IP changed for %s: %s → %s", targetAddr, oldIP, resolvedIP)
		}
	}

	// Update cache
	expiresAt := now.Add(dc.ttl)
	
	if exists {
		entry.mu.Lock()
		entry.ResolvedIP = resolvedIP
		entry.CachedAt = now
		entry.ExpiresAt = expiresAt
		entry.mu.Unlock()
	} else {
		dc.mu.Lock()
		dc.entries[targetAddr] = &DNSCacheEntry{
			ResolvedIP:  resolvedIP,
			OriginalDNS: targetAddr,
			CachedAt:    now,
			ExpiresAt:   expiresAt,
		}
		dc.mu.Unlock()
		log.Printf("📝 DNS cached: %s → %s (expires in %v)", targetAddr, resolvedIP, dc.ttl)
	}

	return resolvedIP, ipChanged, nil
}

// GetCachedIP returns the cached IP without resolving, or empty string if not cached
func (dc *DNSCache) GetCachedIP(targetAddr string) string {
	dc.mu.RLock()
	entry, exists := dc.entries[targetAddr]
	dc.mu.RUnlock()

	if !exists {
		return ""
	}

	entry.mu.RLock()
	defer entry.mu.RUnlock()
	return entry.ResolvedIP
}

// InvalidateCache forces a cache entry to be re-resolved on next ping
func (dc *DNSCache) InvalidateCache(targetAddr string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	
	if entry, exists := dc.entries[targetAddr]; exists {
		entry.mu.Lock()
		entry.ExpiresAt = time.Now().Add(-1 * time.Minute) // Expire it
		entry.mu.Unlock()
		log.Printf("🗑️  DNS cache invalidated for %s", targetAddr)
	}
}

// CleanupExpired removes expired cache entries (called periodically)
func (dc *DNSCache) CleanupExpired() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	now := time.Now()
	removed := 0
	
	for addr, entry := range dc.entries {
		entry.mu.RLock()
		expired := now.After(entry.ExpiresAt)
		entry.mu.RUnlock()
		
		if expired {
			delete(dc.entries, addr)
			removed++
		}
	}
	
	if removed > 0 {
		log.Printf("🧹 Cleaned up %d expired DNS cache entries", removed)
	}
}

// GetCacheInfo returns cache statistics for monitoring
func (dc *DNSCache) GetCacheInfo() map[string]interface{} {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	info := make(map[string]interface{})
	info["total_entries"] = len(dc.entries)
	info["ttl_minutes"] = dc.ttl.Minutes()
	
	entries := make([]map[string]interface{}, 0)
	now := time.Now()
	
	for _, entry := range dc.entries {
		entry.mu.RLock()
		entryInfo := map[string]interface{}{
			"dns":            entry.OriginalDNS,
			"ip":             entry.ResolvedIP,
			"cached_at":      entry.CachedAt.Format("2006-01-02 15:04:05"),
			"expires_in_sec": int(entry.ExpiresAt.Sub(now).Seconds()),
		}
		entry.mu.RUnlock()
		entries = append(entries, entryInfo)
	}
	
	info["entries"] = entries
	return info
}

