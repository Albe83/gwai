package adminui

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Albe83/gwai/internal/platform"
)

const (
	sessionIDBytes           = 32
	csrfTokenBytes           = 32
	operationTokenBytes      = 24
	defaultMaxSessions       = 4096
	maxPendingKeyCreations   = 8
	keyCreationTokenLifetime = 15 * time.Minute
)

type flashMessage struct {
	Kind    string
	Message string
}

type storedSession struct {
	Authenticated    bool
	CSRFToken        string
	ExpiresAt        time.Time
	Flashes          []flashMessage
	KeyCreationToken map[string]time.Time
}

type sessionSnapshot struct {
	ID            string
	Authenticated bool
	CSRFToken     string
	ExpiresAt     time.Time
}

type sessionStore struct {
	mu          sync.Mutex
	sessions    map[string]*storedSession
	now         func() time.Time
	random      io.Reader
	ttl         time.Duration
	maxSessions int
}

func newSessionStore(now func() time.Time, random io.Reader, ttl time.Duration) *sessionStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if random == nil {
		random = rand.Reader
	}
	return &sessionStore{
		sessions: make(map[string]*storedSession), now: now, random: random,
		ttl: ttl, maxSessions: defaultMaxSessions,
	}
}

func randomToken(reader io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *sessionStore) cleanupLocked(now time.Time) {
	for id, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, id)
			continue
		}
		for token, expiresAt := range session.KeyCreationToken {
			if !expiresAt.After(now) {
				delete(session.KeyCreationToken, token)
			}
		}
	}
}

func (s *sessionStore) create(authenticated bool) (sessionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupLocked(now)
	if len(s.sessions) >= s.maxSessions {
		return sessionSnapshot{}, errors.New("session capacity reached")
	}
	for range 4 {
		id, err := randomToken(s.random, sessionIDBytes)
		if err != nil {
			return sessionSnapshot{}, err
		}
		if _, exists := s.sessions[id]; exists {
			continue
		}
		csrf, err := randomToken(s.random, csrfTokenBytes)
		if err != nil {
			return sessionSnapshot{}, err
		}
		expires := now.Add(s.ttl)
		s.sessions[id] = &storedSession{
			Authenticated: authenticated, CSRFToken: csrf, ExpiresAt: expires,
			KeyCreationToken: make(map[string]time.Time),
		}
		return sessionSnapshot{ID: id, Authenticated: authenticated, CSRFToken: csrf, ExpiresAt: expires}, nil
	}
	return sessionSnapshot{}, errors.New("could not allocate a unique session")
}

func (s *sessionStore) load(id string) (sessionSnapshot, bool) {
	if id == "" {
		return sessionSnapshot{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupLocked(now)
	stored, ok := s.sessions[id]
	if !ok {
		return sessionSnapshot{}, false
	}
	return sessionSnapshot{
		ID: id, Authenticated: stored.Authenticated,
		CSRFToken: stored.CSRFToken, ExpiresAt: stored.ExpiresAt,
	}, true
}

func (s *sessionStore) verifyCSRF(id, candidate string) bool {
	snapshot, ok := s.load(id)
	return ok && candidate != "" && platform.SecureEqual(snapshot.CSRFToken, candidate)
}

func (s *sessionStore) destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *sessionStore) addFlash(id string, flash flashMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.sessions[id]
	if !ok || !stored.ExpiresAt.After(s.now()) {
		return
	}
	stored.Flashes = append(stored.Flashes, flash)
}

func (s *sessionStore) takeFlashes(id string) []flashMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.sessions[id]
	if !ok || !stored.ExpiresAt.After(s.now()) {
		return nil
	}
	result := append([]flashMessage(nil), stored.Flashes...)
	stored.Flashes = nil
	return result
}

func (s *sessionStore) issueKeyCreationToken(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.sessions[id]
	now := s.now()
	if !ok || !stored.Authenticated || !stored.ExpiresAt.After(now) {
		return "", errors.New("authenticated session is required")
	}
	for token, expiresAt := range stored.KeyCreationToken {
		if !expiresAt.After(now) {
			delete(stored.KeyCreationToken, token)
		}
	}
	if len(stored.KeyCreationToken) >= maxPendingKeyCreations {
		return "", errors.New("too many pending key-creation forms")
	}
	for range 4 {
		token, err := randomToken(s.random, operationTokenBytes)
		if err != nil {
			return "", err
		}
		if _, exists := stored.KeyCreationToken[token]; exists {
			continue
		}
		expires := now.Add(keyCreationTokenLifetime)
		if expires.After(stored.ExpiresAt) {
			expires = stored.ExpiresAt
		}
		stored.KeyCreationToken[token] = expires
		return token, nil
	}
	return "", errors.New("could not allocate a unique key-creation token")
}

func (s *sessionStore) consumeKeyCreationToken(id, candidate string) bool {
	if candidate == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.sessions[id]
	now := s.now()
	if !ok || !stored.Authenticated || !stored.ExpiresAt.After(now) {
		return false
	}
	expiresAt, ok := stored.KeyCreationToken[candidate]
	if !ok || !expiresAt.After(now) {
		delete(stored.KeyCreationToken, candidate)
		return false
	}
	delete(stored.KeyCreationToken, candidate)
	return true
}

func (s *sessionStore) randomValue(size int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return randomToken(s.random, size)
}
