package main

import (
	"fmt"
	"log"
	"time"

	"github.com/go-ping/ping"
)

// pingTarget pings a single target and returns success status, packet loss, and average RTT
func (pm *PingMonitor) pingTarget(target Target) (bool, int, float64) {
	// Resolve target address using DNS cache (handles both IPs and DNS names)
	resolvedAddr, ipChanged, err := pm.dnsCache.Resolve(target.TargetAddr)
	if err != nil {
		log.Printf("⚠️  DNS resolution failed for %s: %v", formatTargetInfo(target), err)
		return false, 100, 0
	}
	
	// Log DDNS IP changes
	if ipChanged {
		logMsg := fmt.Sprintf("🔄 DDNS IP changed for %s: now resolves to %s", 
			formatTargetInfo(target), resolvedAddr)
		log.Printf(logMsg)
		pm.addLog(logMsg)
		pm.recordEvent(target, "ddns_ip_changed", 0, 0, 0)
	}
	
	pinger, err := ping.NewPinger(resolvedAddr)
	if err != nil {
		log.Printf("⚠️  Error creating pinger for %s (%s): %v", 
			formatTargetInfo(target), resolvedAddr, err)
		return false, 100, 0
	}

	pinger.Count = pm.config.PingCount
	pinger.Timeout = pm.getTargetTimeout(target)
	pinger.SetPrivileged(pm.config.UseRawSockets) // Configurable: raw sockets for better performance

	err = pinger.Run()
	if err != nil {
		log.Printf("⚠️  Error pinging %s: %v", formatTargetInfo(target), err)
		return false, 100, 0
	}

	stats := pinger.Statistics()
	packetsRecv := stats.PacketsRecv
	packetsSent := stats.PacketsSent

	var packetLossPercent int
	if packetsSent > 0 {
		packetLossPercent = int(100 * (packetsSent - packetsRecv) / packetsSent)
	} else {
		packetLossPercent = 100
	}

	success := packetsRecv > 0
	avgRttMs := float64(stats.AvgRtt) / float64(time.Millisecond)

	// Store latest latency and history (only if valid)
	pm.mu.Lock()
	if success && avgRttMs > 0 {
		pm.lastLatency[target.TargetAddr] = avgRttMs
	} else if success && avgRttMs == 0 {
		// Log when we get a successful ping but 0 latency (rare edge case)
		log.Printf("⚠️  %s: Successful ping but zero latency (packets recv: %d, sent: %d)", 
			formatTargetInfo(target), packetsRecv, packetsSent)
	}
	
	// Record latency point for graphs
	point := LatencyPoint{
		Timestamp:  time.Now(),
		LatencyMs:  avgRttMs,
		Success:    success,
		PacketLoss: packetLossPercent,
	}
	pm.latencyHistory[target.TargetAddr] = append(pm.latencyHistory[target.TargetAddr], point)
	
	// Keep only last 24 hours of data (calculated based on interval)
	maxPoints := (24 * 60 * 60) / pm.config.PingIntervalSeconds // ~4320 points at 20s interval
	if len(pm.latencyHistory[target.TargetAddr]) > maxPoints {
		pm.latencyHistory[target.TargetAddr] = pm.latencyHistory[target.TargetAddr][len(pm.latencyHistory[target.TargetAddr])-maxPoints:]
	}
	pm.mu.Unlock()

	// Update statistics
	pm.updateTargetStats(target, success, packetLossPercent, avgRttMs)

	// Log the result
	if success {
		logMsg := fmt.Sprintf("✓ %s - %d/%d packets received (%.0f%% loss), avg %.2fms",
			formatTargetInfo(target), packetsRecv, packetsSent, float64(packetLossPercent), avgRttMs)
		log.Println(logMsg)
		pm.addLog(logMsg)
	} else {
		logMsg := fmt.Sprintf("✗ %s - 0/%d packets received (100%% loss)",
			formatTargetInfo(target), packetsSent)
		log.Println(logMsg)
		pm.addLog(logMsg)
	}

	return success, packetLossPercent, avgRttMs
}

// monitorTarget monitors a single target with graceful degradation
func (pm *PingMonitor) monitorTarget(target Target, now time.Time) {
	// Panic recovery (concurrency now handled by worker pool)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("🆘 Recovered from panic in monitorTarget for %s: %v",
				formatTargetInfo(target), r)
		}
	}()

	success, packetLoss, rttMs := pm.pingTarget(target)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Check if status changed (down/up)
	wasDown := pm.downTargets[target.TargetAddr]
	requiredRecoveryCount := pm.config.RecoveryConfirmationCount

	if !success && !wasDown {
		// Target just went down
		pm.recoveryConsecutiveCount[target.TargetAddr] = 0 // Reset recovery counter
		pm.handleTargetDown(target, packetLoss, now)
	} else if !success && wasDown {
		// Target still down - reset recovery counter
		pm.recoveryConsecutiveCount[target.TargetAddr] = 0
	} else if success && wasDown {
		// Target was down, now responding - check recovery confirmation
		pm.recoveryConsecutiveCount[target.TargetAddr]++
		currentCount := pm.recoveryConsecutiveCount[target.TargetAddr]
		
		if currentCount >= requiredRecoveryCount {
			// Confirmed recovery
			pm.recoveryConsecutiveCount[target.TargetAddr] = 0
			pm.handleTargetRecovered(target, rttMs, packetLoss)
		} else {
			log.Printf("🔄 %s responding but confirming recovery [%d/%d]",
				formatTargetInfo(target), currentCount, requiredRecoveryCount)
		}
	}

	// Check packet loss and latency thresholds for targets that are up
	if success {
		pm.checkPacketLossThreshold(target, packetLoss, rttMs, now)
		pm.checkLatencyThreshold(target, rttMs, packetLoss, now)
	}
}

// handleTargetDown handles when a target goes down
func (pm *PingMonitor) handleTargetDown(target Target, packetLoss int, now time.Time) {
	pm.downTargets[target.TargetAddr] = true
	pm.downSince[target.TargetAddr] = now
	logMsg := fmt.Sprintf("🔴 ALERT: %s is now DOWN", formatTargetInfo(target))
	log.Printf(logMsg)
	pm.mu.Unlock()

	pm.addLog(logMsg)
	pm.recordEvent(target, "down", 0, 0, 0)

	if pm.canSendAlert(target, "down") {
		if err := pm.sendEmail(target, "down", 0, packetLoss, 0); err != nil {
			log.Printf("⚠️  Failed to send down notification for %s: %v", target.Name, err)
		} else {
			pm.recordAlert(target, "down")
		}
	}
	pm.mu.Lock()
}

// handleTargetRecovered handles when a target comes back up
func (pm *PingMonitor) handleTargetRecovered(target Target, rttMs float64, packetLoss int) {
	downtime := time.Since(pm.downSince[target.TargetAddr])
	delete(pm.downTargets, target.TargetAddr)
	delete(pm.downSince, target.TargetAddr)
	logMsg := fmt.Sprintf("🟢 RECOVERY: %s is now UP (was down for %s)",
		formatTargetInfo(target), formatDuration(downtime))
	log.Printf(logMsg)
	pm.mu.Unlock()

	pm.addLog(logMsg)
	pm.recordEvent(target, "up", rttMs, 0, downtime)

	if pm.canSendAlert(target, "up") {
		if err := pm.sendEmail(target, "up", rttMs, packetLoss, downtime); err != nil {
			log.Printf("⚠️  Failed to send recovery notification for %s: %v", target.Name, err)
		} else {
			pm.recordAlert(target, "up")
		}
	}
	pm.mu.Lock()
}

// checkPacketLossThreshold checks and handles packet loss threshold violations with consecutive count
func (pm *PingMonitor) checkPacketLossThreshold(target Target, packetLoss int, rttMs float64, now time.Time) {
	packetLossThreshold := pm.getPacketLossThreshold(target)
	hadPacketLoss := pm.packetLossTargets[target.TargetAddr]
	hasPacketLoss := packetLoss >= packetLossThreshold
	requiredConsecutive := pm.config.PacketLossConsecutiveCount

	if hasPacketLoss {
		// Reset recovery counter since packet loss is high
		recoveryKey := target.TargetAddr + "_packetloss"
		pm.recoveryConsecutiveCount[recoveryKey] = 0
		
		// Check if we should trigger rapid confirmation
		if !hadPacketLoss && pm.config.PacketLossRapidConfirmEnabled && pm.packetLossConsecutiveCount[target.TargetAddr] == 0 {
			// First detection - trigger rapid confirmation in background
			log.Printf("🔍 %s packet loss detected (%d%% >= %d%%) - triggering rapid confirmation",
				formatTargetInfo(target), packetLoss, packetLossThreshold)
			
			go pm.rapidConfirmPacketLoss(target, packetLossThreshold, now)
			return // Let rapid confirmation handle the counting
		}
		
		// Increment consecutive packet loss counter
		pm.packetLossConsecutiveCount[target.TargetAddr]++
		currentCount := pm.packetLossConsecutiveCount[target.TargetAddr]

		if !hadPacketLoss && currentCount >= requiredConsecutive {
			// Reached threshold - trigger alert
			pm.packetLossTargets[target.TargetAddr] = true
			pm.packetLossSince[target.TargetAddr] = now
			logMsg := fmt.Sprintf("🟠 ALERT: %s has PACKET LOSS (%d%% >= %d%%) for %d consecutive checks",
				formatTargetInfo(target), packetLoss, packetLossThreshold, currentCount)
			log.Printf(logMsg)
			pm.mu.Unlock()

			pm.addLog(logMsg)
			pm.recordEvent(target, "packet_loss", float64(packetLoss), float64(packetLossThreshold), 0)

			if pm.canSendAlert(target, "packet_loss") {
				if err := pm.sendEmail(target, "packet_loss", rttMs, packetLoss, 0); err != nil {
					log.Printf("⚠️  Failed to send packet loss notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss")
				}
			}
			pm.mu.Lock()
		} else if !hadPacketLoss && currentCount < requiredConsecutive {
			// Packet loss detected but not enough consecutive occurrences yet
			log.Printf("⚠️  %s packet loss detected (%d%% >= %d%%) [%d/%d consecutive]",
				formatTargetInfo(target), packetLoss, packetLossThreshold, currentCount, requiredConsecutive)
		}
	} else {
		// Packet loss is normal - reset alert counter
		if pm.packetLossConsecutiveCount[target.TargetAddr] > 0 {
			log.Printf("✓ %s packet loss returned to normal before alerting threshold (was %d/%d consecutive)",
				formatTargetInfo(target), pm.packetLossConsecutiveCount[target.TargetAddr], requiredConsecutive)
		}
		pm.packetLossConsecutiveCount[target.TargetAddr] = 0

		if hadPacketLoss {
			// Had packet loss, now normal - check recovery confirmation
			recoveryKey := target.TargetAddr + "_packetloss"
			pm.recoveryConsecutiveCount[recoveryKey]++
			currentRecoveryCount := pm.recoveryConsecutiveCount[recoveryKey]
			requiredRecoveryCount := pm.config.RecoveryConfirmationCount
			
			if currentRecoveryCount >= requiredRecoveryCount {
				// Confirmed recovery
				pm.recoveryConsecutiveCount[recoveryKey] = 0
				duration := time.Duration(0)
				if startTime, exists := pm.packetLossSince[target.TargetAddr]; exists {
					duration = time.Since(startTime)
				}
				delete(pm.packetLossTargets, target.TargetAddr)
				delete(pm.packetLossSince, target.TargetAddr)
				log.Printf("🟢 RECOVERY: %s packet loss is now NORMAL (%d%% < %d%%) confirmed after %d checks",
					formatTargetInfo(target), packetLoss, packetLossThreshold, currentRecoveryCount)
				pm.mu.Unlock()

				pm.recordEvent(target, "packet_loss_normal", float64(packetLoss), float64(packetLossThreshold), duration)

				if pm.canSendAlert(target, "packet_loss_normal") {
					if err := pm.sendEmail(target, "packet_loss_normal", rttMs, packetLoss, duration); err != nil {
						log.Printf("⚠️  Failed to send packet loss recovery notification for %s: %v", target.Name, err)
					} else {
						pm.recordAlert(target, "packet_loss_normal")
					}
				}
				pm.mu.Lock()
			} else {
				log.Printf("🔄 %s packet loss normal but confirming recovery [%d/%d]",
					formatTargetInfo(target), currentRecoveryCount, requiredRecoveryCount)
			}
		}
	}
}

// checkLatencyThreshold checks and handles latency threshold violations with consecutive count
func (pm *PingMonitor) checkLatencyThreshold(target Target, rttMs float64, packetLoss int, now time.Time) {
	threshold := pm.getTargetThreshold(target)
	wasSlow := pm.slowTargets[target.TargetAddr]
	isSlow := rttMs > float64(threshold)
	requiredConsecutive := pm.config.HighLatencyConsecutiveCount

	if isSlow {
		// Reset recovery counter since latency is high
		recoveryKey := target.TargetAddr + "_latency"
		pm.recoveryConsecutiveCount[recoveryKey] = 0
		
		// Check if we should trigger rapid confirmation
		if !wasSlow && pm.config.HighLatencyRapidConfirmEnabled && pm.slowConsecutiveCount[target.TargetAddr] == 0 {
			// First detection - trigger rapid confirmation in background
			log.Printf("🔍 %s high latency detected (%.2fms > %dms) - triggering rapid confirmation",
				formatTargetInfo(target), rttMs, threshold)
			
			go pm.rapidConfirmHighLatency(target, threshold, now)
			return // Let rapid confirmation handle the counting
		}
		
		// Increment consecutive high latency counter
		pm.slowConsecutiveCount[target.TargetAddr]++
		currentCount := pm.slowConsecutiveCount[target.TargetAddr]

		if !wasSlow && currentCount >= requiredConsecutive {
			// Reached threshold - trigger alert
			pm.slowTargets[target.TargetAddr] = true
			pm.slowSince[target.TargetAddr] = now
			logMsg := fmt.Sprintf("🟡 ALERT: %s has HIGH LATENCY (%.2fms > %dms) for %d consecutive checks",
				formatTargetInfo(target), rttMs, threshold, currentCount)
			log.Printf(logMsg)
			pm.mu.Unlock()

			pm.addLog(logMsg)
			pm.recordEvent(target, "high_latency", rttMs, float64(threshold), 0)

			if pm.canSendAlert(target, "slow") {
				if err := pm.sendEmail(target, "slow", rttMs, packetLoss, 0); err != nil {
					log.Printf("⚠️  Failed to send high latency notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "slow")
				}
			}
			pm.mu.Lock()
		} else if !wasSlow && currentCount < requiredConsecutive {
			// High latency detected but not enough consecutive occurrences yet
			log.Printf("⚠️  %s high latency detected (%.2fms > %dms) [%d/%d consecutive]",
				formatTargetInfo(target), rttMs, threshold, currentCount, requiredConsecutive)
		}
	} else {
		// Latency is normal - reset alert counter
		if pm.slowConsecutiveCount[target.TargetAddr] > 0 {
			log.Printf("✓ %s latency returned to normal before alerting threshold (was %d/%d consecutive)",
				formatTargetInfo(target), pm.slowConsecutiveCount[target.TargetAddr], requiredConsecutive)
		}
		pm.slowConsecutiveCount[target.TargetAddr] = 0

		if wasSlow {
			// Was slow, now normal - check recovery confirmation
			recoveryKey := target.TargetAddr + "_latency"
			pm.recoveryConsecutiveCount[recoveryKey]++
			currentRecoveryCount := pm.recoveryConsecutiveCount[recoveryKey]
			requiredRecoveryCount := pm.config.RecoveryConfirmationCount
			
			if currentRecoveryCount >= requiredRecoveryCount {
				// Confirmed recovery
				pm.recoveryConsecutiveCount[recoveryKey] = 0
				duration := time.Duration(0)
				if startTime, exists := pm.slowSince[target.TargetAddr]; exists {
					duration = time.Since(startTime)
				}
				delete(pm.slowTargets, target.TargetAddr)
				delete(pm.slowSince, target.TargetAddr)
				log.Printf("🟢 RECOVERY: %s latency is now NORMAL (%.2fms <= %dms) confirmed after %d checks",
					formatTargetInfo(target), rttMs, threshold, currentRecoveryCount)
				pm.mu.Unlock()

				pm.recordEvent(target, "latency_normal", rttMs, float64(threshold), duration)

				if pm.canSendAlert(target, "normal") {
					if err := pm.sendEmail(target, "normal", rttMs, packetLoss, duration); err != nil {
						log.Printf("⚠️  Failed to send latency recovery notification for %s: %v", target.Name, err)
					} else {
						pm.recordAlert(target, "normal")
					}
				}
				pm.mu.Lock()
			} else {
				log.Printf("🔄 %s latency normal but confirming recovery [%d/%d]",
					formatTargetInfo(target), currentRecoveryCount, requiredRecoveryCount)
			}
		}
	}
}

// rapidConfirmHighLatency performs rapid confirmation checks when high latency is first detected
func (pm *PingMonitor) rapidConfirmHighLatency(target Target, threshold int, detectedTime time.Time) {
	// Wait initial delay to let transient spikes settle
	time.Sleep(time.Duration(pm.config.HighLatencyRapidConfirmDelaySeconds) * time.Second)
	
	confirmCount := 0
	totalChecks := pm.config.HighLatencyRapidConfirmCount
	
	log.Printf("⚡ %s rapid confirmation started (%d checks, %ds interval)",
		formatTargetInfo(target), totalChecks, pm.config.HighLatencyRapidConfirmIntervalSeconds)
	
	for i := 0; i < totalChecks; i++ {
		if i > 0 {
			time.Sleep(time.Duration(pm.config.HighLatencyRapidConfirmIntervalSeconds) * time.Second)
		}
		
		// Perform quick ping
		success, _, rttMs := pm.pingTarget(target)
		
		if success && rttMs > float64(threshold) {
			confirmCount++
			log.Printf("⚡ %s rapid check %d/%d: HIGH (%.2fms > %dms)",
				formatTargetInfo(target), i+1, totalChecks, rttMs, threshold)
		} else if success {
			log.Printf("⚡ %s rapid check %d/%d: Normal (%.2fms)",
				formatTargetInfo(target), i+1, totalChecks, rttMs)
		} else {
			log.Printf("⚡ %s rapid check %d/%d: Failed (down)",
				formatTargetInfo(target), i+1, totalChecks)
		}
	}
	
	// Determine if majority confirms high latency
	majorityThreshold := (totalChecks / 2) + 1
	isConfirmed := confirmCount >= majorityThreshold
	
	pm.mu.Lock()
	
	// Check if target state hasn't changed during rapid checks
	if pm.slowTargets[target.TargetAddr] {
		pm.mu.Unlock()
		log.Printf("⚡ %s rapid confirmation completed but target already marked slow", formatTargetInfo(target))
		return
	}
	
	if isConfirmed {
		// Confirmed high latency - increment consecutive counter
		pm.slowConsecutiveCount[target.TargetAddr]++
		log.Printf("✅ %s rapid confirmation: CONFIRMED high latency (%d/%d checks) [%d/%d consecutive]",
			formatTargetInfo(target), confirmCount, totalChecks,
			pm.slowConsecutiveCount[target.TargetAddr], pm.config.HighLatencyConsecutiveCount)
		
		// Check if we've reached the consecutive threshold
		if pm.slowConsecutiveCount[target.TargetAddr] >= pm.config.HighLatencyConsecutiveCount {
			pm.slowTargets[target.TargetAddr] = true
			pm.slowSince[target.TargetAddr] = detectedTime
			logMsg := fmt.Sprintf("🟡 ALERT: %s has HIGH LATENCY (confirmed via rapid checks)",
				formatTargetInfo(target))
			log.Printf(logMsg)
			pm.mu.Unlock()
			
			pm.addLog(logMsg)
			pm.recordEvent(target, "high_latency", float64(threshold), float64(threshold), 0)
			
			if pm.canSendAlert(target, "slow") {
				if err := pm.sendEmail(target, "slow", float64(threshold), 0, 0); err != nil {
					log.Printf("⚠️  Failed to send high latency notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "slow")
				}
			}
			return // Don't unlock again
		}
	} else {
		log.Printf("✅ %s rapid confirmation: NOT confirmed (%d/%d high) - resetting counter",
			formatTargetInfo(target), confirmCount, totalChecks)
		pm.slowConsecutiveCount[target.TargetAddr] = 0
	}
	pm.mu.Unlock()
}

// rapidConfirmPacketLoss performs rapid confirmation checks when packet loss is first detected
func (pm *PingMonitor) rapidConfirmPacketLoss(target Target, lossThreshold int, detectedTime time.Time) {
	// Wait initial delay to let transient spikes settle
	time.Sleep(time.Duration(pm.config.PacketLossRapidConfirmDelaySeconds) * time.Second)
	
	confirmCount := 0
	totalChecks := pm.config.PacketLossRapidConfirmCount
	
	log.Printf("⚡ %s packet loss rapid confirmation started (%d checks, %ds interval)",
		formatTargetInfo(target), totalChecks, pm.config.PacketLossRapidConfirmIntervalSeconds)
	
	for i := 0; i < totalChecks; i++ {
		if i > 0 {
			time.Sleep(time.Duration(pm.config.PacketLossRapidConfirmIntervalSeconds) * time.Second)
		}
		
		// Perform quick ping
		success, packetLoss, _ := pm.pingTarget(target)
		
		if success && packetLoss >= lossThreshold {
			confirmCount++
			log.Printf("⚡ %s rapid check %d/%d: HIGH LOSS (%d%% >= %d%%)",
				formatTargetInfo(target), i+1, totalChecks, packetLoss, lossThreshold)
		} else if success {
			log.Printf("⚡ %s rapid check %d/%d: Normal (%d%% loss)",
				formatTargetInfo(target), i+1, totalChecks, packetLoss)
		} else {
			log.Printf("⚡ %s rapid check %d/%d: Failed (down)",
				formatTargetInfo(target), i+1, totalChecks)
		}
	}
	
	// Determine if majority confirms packet loss
	majorityThreshold := (totalChecks / 2) + 1
	isConfirmed := confirmCount >= majorityThreshold
	
	pm.mu.Lock()
	
	// Check if target state hasn't changed during rapid checks
	if pm.packetLossTargets[target.TargetAddr] {
		pm.mu.Unlock()
		log.Printf("⚡ %s rapid confirmation completed but target already marked with packet loss", formatTargetInfo(target))
		return
	}
	
	if isConfirmed {
		// Confirmed packet loss - increment consecutive counter
		pm.packetLossConsecutiveCount[target.TargetAddr]++
		log.Printf("✅ %s rapid confirmation: CONFIRMED packet loss (%d/%d checks) [%d/%d consecutive]",
			formatTargetInfo(target), confirmCount, totalChecks,
			pm.packetLossConsecutiveCount[target.TargetAddr], pm.config.PacketLossConsecutiveCount)
		
		// Check if we've reached the consecutive threshold
		if pm.packetLossConsecutiveCount[target.TargetAddr] >= pm.config.PacketLossConsecutiveCount {
			pm.packetLossTargets[target.TargetAddr] = true
			pm.packetLossSince[target.TargetAddr] = detectedTime
			logMsg := fmt.Sprintf("🟠 ALERT: %s has PACKET LOSS (confirmed via rapid checks)",
				formatTargetInfo(target))
			log.Printf(logMsg)
			pm.mu.Unlock()
			
			pm.addLog(logMsg)
			pm.recordEvent(target, "packet_loss", float64(lossThreshold), float64(lossThreshold), 0)
			
			if pm.canSendAlert(target, "packet_loss") {
				if err := pm.sendEmail(target, "packet_loss", 0, lossThreshold, 0); err != nil {
					log.Printf("⚠️  Failed to send packet loss notification for %s: %v", target.Name, err)
				} else {
					pm.recordAlert(target, "packet_loss")
				}
			}
			return // Don't unlock again
		}
	} else {
		log.Printf("✅ %s rapid confirmation: NOT confirmed (%d/%d high loss) - resetting counter",
			formatTargetInfo(target), confirmCount, totalChecks)
		pm.packetLossConsecutiveCount[target.TargetAddr] = 0
	}
	pm.mu.Unlock()
}
