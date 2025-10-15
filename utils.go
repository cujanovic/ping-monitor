package main

import (
	"fmt"
	"net"
	"time"
)

// isIPAddress checks if a string is an IP address (IPv4 or IPv6)
func isIPAddress(addr string) bool {
	return net.ParseIP(addr) != nil
}

// getTargetLabel returns "IP" or "Domain" based on the target address type
func getTargetLabel(addr string) string {
	if isIPAddress(addr) {
		return "IP"
	}
	return "Domain"
}

// formatTargetInfo formats target info as "name (IP: x.x.x.x)" or "name (Domain: example.com)"
func formatTargetInfo(target Target) string {
	label := getTargetLabel(target.TargetAddr)
	return fmt.Sprintf("%s (%s: %s)", target.Name, label, target.TargetAddr)
}

// formatNumber formats a number with commas for readability
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	str := fmt.Sprintf("%d", n)
	result := ""
	for i, digit := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(digit)
	}
	return result
}

// formatDuration formats a duration in a human-readable and precise way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		if d < time.Second {
			return fmt.Sprintf("%d milliseconds", d.Milliseconds())
		}
		return fmt.Sprintf("%.1f seconds", d.Seconds())
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := d.Seconds() - float64(minutes*60)
		return fmt.Sprintf("%d minutes %.0f seconds", minutes, seconds)
	} else if d < 24*time.Hour {
		hours := int(d.Hours())
		remainingMinutes := d.Minutes() - float64(hours*60)
		minutes := int(remainingMinutes)
		seconds := d.Seconds() - float64(hours*3600) - float64(minutes*60)
		if minutes == 0 && seconds == 0 {
			return fmt.Sprintf("%d hours", hours)
		} else if seconds == 0 {
			return fmt.Sprintf("%d hours %d minutes", hours, minutes)
		}
		return fmt.Sprintf("%d hours %d minutes %.0f seconds", hours, minutes, seconds)
	} else {
		days := int(d.Hours()) / 24
		remainingHours := d.Hours() - float64(days*24)
		hours := int(remainingHours)
		remainingMinutes := d.Minutes() - float64(days*24*60) - float64(hours*60)
		minutes := int(remainingMinutes)
		if hours == 0 && minutes == 0 {
			return fmt.Sprintf("%d days", days)
		} else if minutes == 0 {
			return fmt.Sprintf("%d days %d hours", days, hours)
		}
		return fmt.Sprintf("%d days %d hours %d minutes", days, hours, minutes)
	}
}

// formatEvent formats an event record in a human-readable way
func formatEvent(event EventRecord) string {
	switch event.EventType {
	case "down":
		return "Target went DOWN"
	case "up":
		if event.Duration > 0 {
			return fmt.Sprintf("Target recovered (downtime: %s)", formatDuration(event.Duration))
		}
		return "Target recovered"
	case "packet_loss":
		return fmt.Sprintf("Packet loss: %.0f%% (threshold: %.0f%%)", event.Value, event.Threshold)
	case "packet_loss_normal":
		return fmt.Sprintf("Packet loss recovered: %.0f%%", event.Value)
	case "high_latency":
		return fmt.Sprintf("High latency: %.2fms (threshold: %.0fms)", event.Value, event.Threshold)
	case "latency_normal":
		return fmt.Sprintf("Latency recovered: %.2fms", event.Value)
	default:
		return fmt.Sprintf("%s: %.2f", event.EventType, event.Value)
	}
}

