package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	// CLI flags
	setPassword := flag.Bool("set-password", false, "Generate Argon2id hash for a new password")
	flag.Parse()
	
	// Handle password generation
	if *setPassword {
		handlePasswordGeneration()
		return
	}

	// Initialize structured logger
	initLogger()
	LogInfo("Ping Monitor Service Starting")
	
	// Load configuration
	config, err := loadConfig("config.json")
	if err != nil {
		LogError("Failed to load configuration", "error", err)
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}

	// Set defaults for optional fields
	if config.StateFilePath == "" {
		config.StateFilePath = "./state.json"
	}
	if config.StateSaveIntervalSeconds <= 0 {
		config.StateSaveIntervalSeconds = 5 // Default throttle: max 1 save per 5 seconds
	}
	if config.RecentEventsBufferSize <= 0 {
		config.RecentEventsBufferSize = 1000
	}
	if config.DNSCacheTTLMinutes <= 0 {
		config.DNSCacheTTLMinutes = 60
	}
	if config.StaggerWindowSeconds <= 0 {
		config.StaggerWindowSeconds = 20 // Default: distribute pings over 20 seconds
	}

	// Validate configuration
	if err := ValidateConfig(config); err != nil {
		LogError("Configuration validation failed", "error", err)
		log.Fatalf("❌ %v", err)
	}
	
	LogInfo("Configuration loaded and validated successfully", 
		"targets", len(config.Targets),
		"ping_interval", config.PingIntervalSeconds,
		"http_enabled", config.HTTPEnabled)
	
	// Initialize and start ping monitor
	monitor := NewPingMonitor(config)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-sigChan
		LogInfo("Shutting down gracefully")
		log.Printf("👋 Shutting down gracefully...")
		
		// Stop monitor (this will save state and clean up all goroutines)
		monitor.Stop()
		
		os.Exit(0)
	}()

	monitor.Start()
}

// handlePasswordGeneration generates an Argon2id hash for a password
func handlePasswordGeneration() {
	fmt.Println("🔐 Password Hash Generator")
	fmt.Println()
	
	reader := bufio.NewReader(os.Stdin)
	
	// Get password
	fmt.Print("Enter password: ")
	password1, _ := reader.ReadString('\n')
	password1 = strings.TrimSpace(password1)
	
	if password1 == "" {
		fmt.Println("❌ Password cannot be empty")
		os.Exit(1)
	}
	
	if len(password1) < 8 {
		fmt.Println("⚠️  Warning: Password is short. Recommended minimum: 8 characters")
	}
	
	// Confirm password
	fmt.Print("Confirm password: ")
	password2, _ := reader.ReadString('\n')
	password2 = strings.TrimSpace(password2)
	
	if password1 != password2 {
		fmt.Println("❌ Passwords do not match")
		os.Exit(1)
	}
	
	// Generate hash with default parameters
	fmt.Println()
	fmt.Println("Generating Argon2id hash (this may take a moment)...")
	hash, err := GenerateArgon2Hash(password1, 65536, 3, 4)
	if err != nil {
		fmt.Printf("❌ Failed to generate hash: %v\n", err)
		os.Exit(1)
	}
	
	fmt.Println()
	fmt.Println("✅ Password hash generated successfully!")
	fmt.Println()
	fmt.Println("Add this to your config.json:")
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf(`"auth_enabled": true,
"password_hash": "%s",
"argon2_memory": 65536,
"argon2_time": 3,
"argon2_threads": 4,
"session_timeout_minutes": 60,
"max_login_attempts": 5,
"lockout_duration_minutes": 15
`, hash)
	fmt.Println("─────────────────────────────────────────")
	fmt.Println()
	fmt.Println("Then restart the service to apply changes.")
}
