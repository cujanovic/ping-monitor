package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config represents the configuration structure
type Config struct {
	PingIntervalSeconds        int      `json:"ping_interval_seconds"`
	PingCount                  int      `json:"ping_count"`
	PingTimeThresholdMs        int      `json:"ping_time_threshold_ms"`
	PacketLossThresholdPercent int      `json:"packet_loss_threshold_percent"`
	AlertCooldownMinutes       int      `json:"alert_cooldown_minutes"`
	EmailRateLimitPerHour      int      `json:"email_rate_limit_per_hour"`
	MaxConcurrentPings         int      `json:"max_concurrent_pings"`
	DefaultTimeoutSeconds      int      `json:"default_timeout_seconds"`
	ReportTimeOffsetHours      int      `json:"report_time_offset_hours"`
	SummaryReportEnabled       bool     `json:"summary_report_enabled"`
	SummaryReportSchedule      string   `json:"summary_report_schedule"`
	SummaryReportTime          string   `json:"summary_report_time"`
	HTTPEnabled                bool     `json:"http_enabled"`
	HTTPListen                 string   `json:"http_listen"`
	HTTPLogLines               int      `json:"http_log_lines"`
	HTTPRateLimitPerMinute     int      `json:"http_rate_limit_per_minute"`
	ReportsDirectory           string   `json:"reports_directory"`
	ReportsKeepCount           int      `json:"reports_keep_count"`
	LogBufferFlushSeconds      int      `json:"log_buffer_flush_seconds"`
	Email                      Email    `json:"email"`
	Targets                    []Target `json:"targets"`
}

type Email struct {
	APIKey string `json:"api_key"`
	From   string `json:"from"`
	To     string `json:"to"`
}

type Target struct {
	Name                       string `json:"name"`
	TargetAddr                 string `json:"target"`
	PingThresholdMs            int    `json:"ping_time_threshold_ms,omitempty"`
	PacketLossThresholdPercent int    `json:"packet_loss_threshold_percent,omitempty"`
	TimeoutSeconds             int    `json:"timeout_seconds,omitempty"`
}

// loadConfig loads configuration from a JSON file
func loadConfig(filename string) (Config, error) {
	var config Config

	data, err := os.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %v", err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("failed to parse config file: %v", err)
	}

	return config, nil
}

// ValidateConfig validates the configuration
func ValidateConfig(config Config) error {
	errors := make([]string, 0)

	// Validate basic settings
	if config.PingIntervalSeconds <= 0 {
		errors = append(errors, "ping_interval_seconds must be greater than 0")
	}
	if config.PingIntervalSeconds < 5 {
		errors = append(errors, "ping_interval_seconds should be at least 5 seconds for reliability")
	}
	if config.PingCount < 1 {
		errors = append(errors, "ping_count must be at least 1")
	}
	if config.PingCount > 10 {
		errors = append(errors, "ping_count should not exceed 10 for performance reasons")
	}

	// Validate thresholds
	if config.PingTimeThresholdMs < 0 {
		errors = append(errors, "ping_time_threshold_ms cannot be negative")
	}
	if config.PacketLossThresholdPercent < 0 || config.PacketLossThresholdPercent > 100 {
		errors = append(errors, "packet_loss_threshold_percent must be between 0 and 100")
	}
	if config.AlertCooldownMinutes < 0 {
		errors = append(errors, "alert_cooldown_minutes cannot be negative")
	}
	if config.EmailRateLimitPerHour < 1 {
		errors = append(errors, "email_rate_limit_per_hour must be at least 1")
	}
	if config.EmailRateLimitPerHour > 300 {
		errors = append(errors, "email_rate_limit_per_hour exceeds Brevo free tier limit (300/day)")
	}
	if config.MaxConcurrentPings < 1 {
		errors = append(errors, "max_concurrent_pings must be at least 1")
	}
	if config.MaxConcurrentPings > 50 {
		errors = append(errors, "max_concurrent_pings should not exceed 50 for stability")
	}
	if config.DefaultTimeoutSeconds < 1 || config.DefaultTimeoutSeconds > 60 {
		errors = append(errors, "default_timeout_seconds must be between 1 and 60")
	}

	// Validate email config
	if config.Email.APIKey == "" || config.Email.APIKey == "your-brevo-api-key-here" {
		errors = append(errors, "email.api_key must be configured with a valid Brevo API key")
	}
	if config.Email.From == "" {
		errors = append(errors, "email.from cannot be empty")
	}
	if !strings.Contains(config.Email.From, "@") {
		errors = append(errors, "email.from must be a valid email address")
	}
	if config.Email.To == "" {
		errors = append(errors, "email.to cannot be empty")
	}
	if !strings.Contains(config.Email.To, "@") {
		errors = append(errors, "email.to must be a valid email address")
	}

	// Validate summary report settings
	if config.SummaryReportEnabled {
		if config.SummaryReportSchedule != "daily" && config.SummaryReportSchedule != "weekly" {
			errors = append(errors, "summary_report_schedule must be 'daily' or 'weekly'")
		}
		if config.SummaryReportTime != "" {
			parts := strings.Split(config.SummaryReportTime, ":")
			if len(parts) != 2 {
				errors = append(errors, "summary_report_time must be in HH:MM format")
			}
		}
	}

	// Validate targets
	if len(config.Targets) == 0 {
		errors = append(errors, "at least one target must be configured")
	}
	if len(config.Targets) > 1000 {
		errors = append(errors, "maximum 1000 targets supported")
	}

	targetNames := make(map[string]bool)
	targetAddrs := make(map[string]bool)

	for i, target := range config.Targets {
		// Validate target name
		if target.Name == "" {
			errors = append(errors, fmt.Sprintf("target[%d].name cannot be empty", i))
		}
		if targetNames[target.Name] {
			errors = append(errors, fmt.Sprintf("duplicate target name: %s", target.Name))
		}
		targetNames[target.Name] = true

		// Validate target address
		if target.TargetAddr == "" {
			errors = append(errors, fmt.Sprintf("target[%d].target cannot be empty", i))
		}
		if targetAddrs[target.TargetAddr] {
			errors = append(errors, fmt.Sprintf("duplicate target address: %s", target.TargetAddr))
		}
		targetAddrs[target.TargetAddr] = true

		// Validate per-target thresholds
		if target.PingThresholdMs < 0 {
			errors = append(errors, fmt.Sprintf("target[%d].ping_time_threshold_ms cannot be negative", i))
		}
		if target.PacketLossThresholdPercent < 0 || target.PacketLossThresholdPercent > 100 {
			errors = append(errors, fmt.Sprintf("target[%d].packet_loss_threshold_percent must be between 0 and 100", i))
		}
		if target.TimeoutSeconds < 0 || target.TimeoutSeconds > 60 {
			errors = append(errors, fmt.Sprintf("target[%d].timeout_seconds must be between 0 and 60", i))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

