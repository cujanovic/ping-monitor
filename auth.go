package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Session represents an authenticated session
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// LoginAttempt tracks failed login attempts per IP
type LoginAttempt struct {
	Count      int
	LockedUntil time.Time
}

// SessionManager manages user sessions and login attempts
type SessionManager struct {
	sessions       map[string]*Session
	loginAttempts  map[string]*LoginAttempt
	mu             sync.RWMutex
	config         *Config
}

// NewSessionManager creates a new session manager
func NewSessionManager(config *Config) *SessionManager {
	sm := &SessionManager{
		sessions:      make(map[string]*Session),
		loginAttempts: make(map[string]*LoginAttempt),
		config:        config,
	}
	
	// Start cleanup goroutine
	go sm.cleanupExpiredSessions()
	
	return sm
}

// cleanupExpiredSessions periodically removes expired sessions
func (sm *SessionManager) cleanupExpiredSessions() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()
		for token, session := range sm.sessions {
			if now.After(session.ExpiresAt) {
				delete(sm.sessions, token)
			}
		}
		// Clean up expired lockouts
		for ip, attempt := range sm.loginAttempts {
			if now.After(attempt.LockedUntil) && attempt.Count >= sm.config.MaxLoginAttempts {
				delete(sm.loginAttempts, ip)
			}
		}
		sm.mu.Unlock()
	}
}

// GenerateArgon2Hash generates an Argon2id hash of the password
func GenerateArgon2Hash(password string, memory uint32, time uint32, threads uint8) (string, error) {
	// Generate random salt
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	
	// Generate hash
	hash := argon2.IDKey([]byte(password), salt, time, memory, threads, 32)
	
	// Encode to standard format: $argon2id$v=19$m=memory,t=time,p=threads$salt$hash
	encodedSalt := base64.RawStdEncoding.EncodeToString(salt)
	encodedHash := base64.RawStdEncoding.EncodeToString(hash)
	
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory, time, threads, encodedSalt, encodedHash), nil
}

// VerifyArgon2Hash verifies a password against an Argon2id hash
func VerifyArgon2Hash(password, encodedHash string) (bool, error) {
	// Parse the hash
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, fmt.Errorf("invalid hash format")
	}
	
	if parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid algorithm")
	}
	
	var memory, time uint32
	var threads uint8
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads)
	if err != nil {
		return false, err
	}
	
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	
	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	
	// Generate hash with same parameters
	hash := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(expectedHash)))
	
	// Constant-time comparison
	return subtle.ConstantTimeCompare(hash, expectedHash) == 1, nil
}

// CreateSession creates a new session and returns the token
func (sm *SessionManager) CreateSession() (string, error) {
	// Generate random session token (32 bytes)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)
	
	// Create session
	session := &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(time.Duration(sm.config.SessionTimeoutMinutes) * time.Minute),
	}
	
	sm.mu.Lock()
	sm.sessions[token] = session
	sm.mu.Unlock()
	
	return token, nil
}

// ValidateSession checks if a session token is valid
func (sm *SessionManager) ValidateSession(token string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	session, exists := sm.sessions[token]
	if !exists {
		return false
	}
	
	return time.Now().Before(session.ExpiresAt)
}

// DeleteSession removes a session
func (sm *SessionManager) DeleteSession(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

// RecordFailedLogin records a failed login attempt
func (sm *SessionManager) RecordFailedLogin(ip string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	attempt, exists := sm.loginAttempts[ip]
	if !exists {
		attempt = &LoginAttempt{Count: 0}
		sm.loginAttempts[ip] = attempt
	}
	
	attempt.Count++
	
	if attempt.Count >= sm.config.MaxLoginAttempts {
		attempt.LockedUntil = time.Now().Add(time.Duration(sm.config.LockoutDurationMinutes) * time.Minute)
		log.Printf("🔒 IP %s locked out after %d failed login attempts", ip, attempt.Count)
	}
}

// IsLockedOut checks if an IP is locked out
func (sm *SessionManager) IsLockedOut(ip string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	attempt, exists := sm.loginAttempts[ip]
	if !exists {
		return false
	}
	
	if attempt.Count >= sm.config.MaxLoginAttempts {
		if time.Now().Before(attempt.LockedUntil) {
			return true
		}
		// Lockout expired, reset
		return false
	}
	
	return false
}

// ResetLoginAttempts resets login attempts for an IP
func (sm *SessionManager) ResetLoginAttempts(ip string) {
	sm.mu.Lock()
	delete(sm.loginAttempts, ip)
	sm.mu.Unlock()
}

// validateReturnURL validates that a return URL is safe (no open redirect)
func (pm *PingMonitor) validateReturnURL(returnURL string) string {
	// Default to root if empty
	if returnURL == "" {
		return "/"
	}
	
	// Comprehensive validation in a single check to satisfy static analysis
	// Check: must start with /, but second char must not be / or \
	// Also check for schemes (:), backslashes, and newlines
	if len(returnURL) < 1 || 
	   returnURL[0] != '/' ||                           // Must start with /
	   (len(returnURL) >= 2 && (returnURL[1] == '/' || returnURL[1] == '\\')) || // Second char not / or \
	   strings.Contains(returnURL, "\\") ||              // No backslashes anywhere
	   strings.Contains(returnURL, ":") ||               // No schemes (http:, javascript:)
	   strings.ContainsAny(returnURL, "\r\n") {          // No newlines
		log.Printf("⚠️  Invalid return URL (failed security checks): %s", returnURL)
		return "/"
	}
	
	// Extract base path (without query params) for whitelist validation
	pathWithoutQuery := returnURL
	if idx := strings.Index(returnURL, "?"); idx != -1 {
		pathWithoutQuery = returnURL[:idx]
	}
	
	// Explicit whitelist: only allow known-safe paths
	switch pathWithoutQuery {
	case "/", "/reports", "/report_now", "/report_all":
		return returnURL
	default:
		log.Printf("⚠️  Invalid return URL (not in whitelist): %s", returnURL)
		return "/"
	}
}

// AuthMiddleware is middleware that requires authentication
func (pm *PingMonitor) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip if auth is disabled
		if !pm.config.AuthEnabled {
			next(w, r)
			return
		}
		
		// Check for session cookie
		cookie, err := r.Cookie("session")
		if err != nil || !pm.sessionManager.ValidateSession(cookie.Value) {
			// Redirect to login with return URL (validated)
			returnURL := r.URL.Path
			if r.URL.RawQuery != "" {
				returnURL += "?" + r.URL.RawQuery
			}
			// Validate the return URL before using it
			safeReturnURL := pm.validateReturnURL(returnURL)
			
			// Construct login redirect URL with validated return parameter
			// Using url.Values to properly encode the parameter
			loginURL := "/login"
			if safeReturnURL != "/" {
				values := url.Values{}
				values.Set("return", safeReturnURL)
				loginURL = "/login?" + values.Encode()
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		
		next(w, r)
	}
}

// handleLogin handles the login page and form submission
func (pm *PingMonitor) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Show login page
		returnURL := r.URL.Query().Get("return")
		// Validate return URL to prevent open redirect
		safeReturnURL := pm.validateReturnURL(returnURL)
		
		data := struct {
			Error     string
			ReturnURL string
		}{
			ReturnURL: safeReturnURL,
		}
		
		if err := pm.templates.ExecuteTemplate(w, "login.html", data); err != nil {
			http.Error(w, "Template error", http.StatusInternalServerError)
			log.Printf("⚠️  Template error: %v", err)
		}
		return
	}
	
	if r.Method == http.MethodPost {
		// Get client IP
		ip := getClientIP(r)
		
		// Check if locked out
		if pm.sessionManager.IsLockedOut(ip) {
			// Validate return URL
			safeReturnURL := pm.validateReturnURL(r.FormValue("return"))
			
			data := struct {
				Error     string
				ReturnURL string
			}{
				Error:     "Too many failed attempts. Please try again later.",
				ReturnURL: safeReturnURL,
			}
			w.WriteHeader(http.StatusTooManyRequests)
			pm.templates.ExecuteTemplate(w, "login.html", data)
			return
		}
		
		// Verify password
		password := r.FormValue("password")
		valid, err := VerifyArgon2Hash(password, pm.config.PasswordHash)
		if err != nil || !valid {
			pm.sessionManager.RecordFailedLogin(ip)
			log.Printf("⚠️  Failed login attempt from %s", ip)
			
			// Validate return URL
			safeReturnURL := pm.validateReturnURL(r.FormValue("return"))
			
			data := struct {
				Error     string
				ReturnURL string
			}{
				Error:     "Invalid password",
				ReturnURL: safeReturnURL,
			}
			w.WriteHeader(http.StatusUnauthorized)
			pm.templates.ExecuteTemplate(w, "login.html", data)
			return
		}
		
		// Reset login attempts on successful login
		pm.sessionManager.ResetLoginAttempts(ip)
		
		// Create session
		token, err := pm.sessionManager.CreateSession()
		if err != nil {
			http.Error(w, "Failed to create session", http.StatusInternalServerError)
			return
		}
		
		// Set cookie (HTTP-only, SameSite, no Secure flag for HTTP/WireGuard)
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			MaxAge:   pm.config.SessionTimeoutMinutes * 60,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		
		log.Printf("✅ Successful login from %s", ip)
		
		// Redirect to return URL (validated to prevent open redirect)
		returnURL := r.FormValue("return")
		safeReturnURL := pm.validateReturnURL(returnURL)
		
		// Extract base path and query string
		basePath := safeReturnURL
		queryString := ""
		if idx := strings.Index(safeReturnURL, "?"); idx != -1 {
			basePath = safeReturnURL[:idx]
			queryString = safeReturnURL[idx:] // includes the '?'
		}
		
		// Reconstruct URL from validated constant path + query string
		// This ensures CodeQL can verify the base path is a constant
		var redirectURL string
		switch basePath {
		case "/":
			redirectURL = "/" + queryString
		case "/reports":
			redirectURL = "/reports" + queryString
		case "/report_now":
			redirectURL = "/report_now" + queryString
		case "/report_all":
			redirectURL = "/report_all" + queryString
		default:
			// If validation somehow failed, redirect to root
			redirectURL = "/"
		}
		
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}
	
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleLogout handles logout
func (pm *PingMonitor) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get session cookie
	cookie, err := r.Cookie("session")
	if err == nil {
		pm.sessionManager.DeleteSession(cookie.Value)
	}
	
	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

