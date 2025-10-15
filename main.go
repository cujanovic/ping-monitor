package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.Printf("🎯 Ping Monitor Service Starting...")
	
	// Load configuration
	config, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}

	// Validate configuration
	if err := ValidateConfig(config); err != nil {
		log.Fatalf("❌ %v", err)
	}
	
	log.Printf("✅ Configuration loaded and validated successfully")
	
	// Initialize and start ping monitor
	monitor := NewPingMonitor(config)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-sigChan
		log.Printf("👋 Shutting down gracefully...")
		os.Exit(0)
	}()

	monitor.Start()
}
