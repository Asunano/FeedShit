package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"sync"
	"time"
)

// ========== Session-based Auth ==========

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]sessionEntry
	ttl      time.Duration
}

type sessionEntry struct {
	username  string
	role      string
	expiry    time.Time
	csrfToken string
}

func NewSessionManager() *SessionManager {
	return NewSessionManagerWithTTL(24 * time.Hour)
}

// NewSessionManagerWithTTL creates a session manager with a configurable TTL.
func NewSessionManagerWithTTL(ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sm := &SessionManager{sessions: make(map[string]sessionEntry), ttl: ttl}
	go sm.cleanupLoop()
	return sm
}

func (sm *SessionManager) Create(username, role string) string {
	token := generateToken(32)
	csrf := generateToken(32)
	sm.mu.Lock()
	sm.sessions[token] = sessionEntry{
		username:  username,
		role:      role,
		expiry:    time.Now().Add(sm.ttl),
		csrfToken: csrf,
	}
	sm.mu.Unlock()
	return token
}

func (sm *SessionManager) Validate(token string) (username, role string, ok bool) {
	sm.mu.RLock()
	entry, exists := sm.sessions[token]
	sm.mu.RUnlock()
	if !exists || time.Now().After(entry.expiry) {
		if exists {
			sm.mu.Lock()
			delete(sm.sessions, token)
			sm.mu.Unlock()
		}
		return "", "", false
	}
	return entry.username, entry.role, true
}

func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

// RevokeUserSessions revokes all sessions for a given username.
// Used after password change to force re-login.
func (sm *SessionManager) RevokeUserSessions(username string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for token, entry := range sm.sessions {
		if entry.username == username {
			delete(sm.sessions, token)
		}
	}
}

// RotateCSRFToken generates a new CSRF token for the given session token
// and returns it. Returns empty string if the session doesn't exist.
func (sm *SessionManager) RotateCSRFToken(sessionToken string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	entry, exists := sm.sessions[sessionToken]
	if !exists {
		return ""
	}
	entry.csrfToken = generateToken(32)
	sm.sessions[sessionToken] = entry
	return entry.csrfToken
}

// GetCSRFToken returns the CSRF token for a given session.
func (sm *SessionManager) GetCSRFToken(token string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if entry, ok := sm.sessions[token]; ok {
		return entry.csrfToken
	}
	return ""
}

func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()
		for token, entry := range sm.sessions {
			if now.After(entry.expiry) {
				delete(sm.sessions, token)
			}
		}
		sm.mu.Unlock()
	}
}

func generateToken(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("Failed to generate session token: %v", err)
	}
	return hex.EncodeToString(b)
}
