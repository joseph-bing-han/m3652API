package m365

import (
	"errors"
	"strings"
	"sync"
	"time"
)

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	ttl      time.Duration
}

type sessionState struct {
	conversationID    string
	processedInputLen int
	lastUsedAt        time.Time
	inFlight          bool
}

func newSessionStore(ttl time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &sessionStore{
		sessions: make(map[string]*sessionState),
		ttl:      ttl,
	}
}

func (s *sessionStore) getOrCreate(sessionKey string) (*sessionState, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil, errors.New("session store: sessionKey is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionKey]
	if !ok {
		st = &sessionState{}
		s.sessions[sessionKey] = st
	}
	st.lastUsedAt = time.Now()
	return st, nil
}

func (s *sessionStore) tryStart(sessionKey string) (*sessionState, func(), error) {
	st, err := s.getOrCreate(sessionKey)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	if st.inFlight {
		s.mu.Unlock()
		return nil, nil, errors.New("session store: request already in-flight for this sessionKey")
	}
	st.inFlight = true
	st.lastUsedAt = time.Now()
	s.mu.Unlock()

	return st, func() {
		s.mu.Lock()
		st.inFlight = false
		st.lastUsedAt = time.Now()
		s.mu.Unlock()
	}, nil
}

func (s *sessionStore) gc() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, st := range s.sessions {
		if st == nil {
			delete(s.sessions, key)
			continue
		}
		if st.inFlight {
			continue
		}
		if now.Sub(st.lastUsedAt) > s.ttl {
			delete(s.sessions, key)
		}
	}
}
