package main

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	sessionCookieName = "builda_session"
	csrfHeaderName    = "X-Builda-CSRF"
	sessionTTL        = 12 * time.Hour

	loginFailureWindow = 5 * time.Minute
	loginFailureLimit  = 5
)

// Session is one authenticated browser session. The session ID lives in an
// HttpOnly cookie; the CSRF token is handed to the page and must be echoed on
// every state-changing browser request.
type Session struct {
	ID      string
	User    string
	CSRF    string
	Expires time.Time
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
}

func newSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = sessionTTL
	}
	return &SessionStore{sessions: map[string]*Session{}, ttl: ttl}
}

func (s *SessionStore) Create(user string) (*Session, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	session := &Session{ID: id, User: user, CSRF: csrf, Expires: time.Now().Add(s.ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.sessions[id] = session
	return session, nil
}

func (s *SessionStore) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(session.Expires) {
		delete(s.sessions, id)
		return nil, false
	}
	copied := *session
	return &copied, true
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// DeleteAll invalidates every session, used when the admin password rotates.
func (s *SessionStore) DeleteAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]*Session{}
}

func (s *SessionStore) sweepLocked() {
	now := time.Now()
	for id, session := range s.sessions {
		if now.After(session.Expires) {
			delete(s.sessions, id)
		}
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// loginLimiter throttles repeated failed logins per client key.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	window   time.Duration
	limit    int
}

func newLoginLimiter(window time.Duration, limit int) *loginLimiter {
	if window <= 0 {
		window = loginFailureWindow
	}
	if limit <= 0 {
		limit = loginFailureLimit
	}
	return &loginLimiter{failures: map[string][]time.Time{}, window: window, limit: limit}
}

// Allowed reports whether another login attempt may be processed.
func (l *loginLimiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recentLocked(key)) < l.limit
}

// Fail records a failed attempt and reports whether the key is now blocked.
func (l *loginLimiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := append(l.recentLocked(key), time.Now())
	l.failures[key] = recent
	return len(recent) >= l.limit
}

// Reset clears the failure history after a successful login.
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *loginLimiter) recentLocked(key string) []time.Time {
	cutoff := time.Now().Add(-l.window)
	kept := make([]time.Time, 0, len(l.failures[key]))
	for _, at := range l.failures[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = kept
	}
	return kept
}
