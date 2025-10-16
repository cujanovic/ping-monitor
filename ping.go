package main

import (
	"fmt"
	"log"
	"time"

	"github.com/go-ping/ping"
)

// pingTarget pings a single target and returns success status, packet loss, and average RTT
func (pm *PingMonitor) pingTarget(target Target) (bool, int, float64) {
	pinger, err := ping.NewPinger(target.TargetAddr)
	if err != nil {
		log.Printf("⚠️  Error creating pinger for %s: %v", formatTargetInfo(target), err)
		return false, 100, 0
	}

	pinger.Count = pm.config.PingCount
	pinger.Timeout = pm.getTargetTimeout(target)
	pinger.SetPrivileged(false)

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

	// Store latest latency
	pm.mu.Lock()
	if success {
		pm.lastLatency[target.TargetAddr] = avgRttMs
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
func (pm *PingMonitor) monitorTarget(target Target) {
	// Acquire semaphore to limit concurrent pings
	pm.semaphore <- struct{}{}
	defer func() {
		<-pm.semaphore
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

	if !success && !wasDown {
		pm.handleTargetDown(target, packetLoss)
	} else if success && wasDown {
		pm.handleTargetRecovered(target, rttMs, packetLoss)
	}

	// Check packet loss and latency thresholds for targets that are up
	if success {
		pm.checkPacketLossThreshold(target, packetLoss, rttMs)
		pm.checkLatencyThreshold(target, rttMs, packetLoss)
	}
}

// handleTargetDown handles when a target goes down
func (pm *PingMonitor) handleTargetDown(target Target, packetLoss int) {
	pm.downTargets[target.TargetAddr] = true
	pm.downSince[target.TargetAddr] = time.Now()
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

// checkPacketLossThreshold checks and handles packet loss threshold violations
func (pm *PingMonitor) checkPacketLossThreshold(target Target, packetLoss int, rttMs float64) {
	packetLossThreshold := pm.getPacketLossThreshold(target)
	hadPacketLoss := pm.packetLossTargets[target.TargetAddr]
	hasPacketLoss := packetLoss >= packetLossThreshold

	if hasPacketLoss && !hadPacketLoss {
		pm.packetLossTargets[target.TargetAddr] = true
		pm.packetLossSince[target.TargetAddr] = time.Now()
		logMsg := fmt.Sprintf("🟠 ALERT: %s has PACKET LOSS (%d%% >= %d%%)",
			formatTargetInfo(target), packetLoss, packetLossThreshold)
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
	} else if !hasPacketLoss && hadPacketLoss {
		duration := time.Duration(0)
		if startTime, exists := pm.packetLossSince[target.TargetAddr]; exists {
			duration = time.Since(startTime)
		}
		delete(pm.packetLossTargets, target.TargetAddr)
		delete(pm.packetLossSince, target.TargetAddr)
		log.Printf("🟢 RECOVERY: %s packet loss is now NORMAL (%d%% < %d%%)",
			formatTargetInfo(target), packetLoss, packetLossThreshold)
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
	}
}

// checkLatencyThreshold checks and handles latency threshold violations
func (pm *PingMonitor) checkLatencyThreshold(target Target, rttMs float64, packetLoss int) {
	threshold := pm.getTargetThreshold(target)
	wasSlow := pm.slowTargets[target.TargetAddr]
	isSlow := rttMs > float64(threshold)

	if isSlow && !wasSlow {
		pm.slowTargets[target.TargetAddr] = true
		pm.slowSince[target.TargetAddr] = time.Now()
		logMsg := fmt.Sprintf("🟡 ALERT: %s has HIGH LATENCY (%.2fms > %dms)",
			formatTargetInfo(target), rttMs, threshold)
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
	} else if !isSlow && wasSlow {
		duration := time.Duration(0)
		if startTime, exists := pm.slowSince[target.TargetAddr]; exists {
			duration = time.Since(startTime)
		}
		delete(pm.slowTargets, target.TargetAddr)
		delete(pm.slowSince, target.TargetAddr)
		log.Printf("🟢 RECOVERY: %s latency is now NORMAL (%.2fms <= %dms)",
			formatTargetInfo(target), rttMs, threshold)
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
	}
}

