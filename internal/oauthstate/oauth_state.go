package oauthstate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

type Pending struct {
	AuthID       string
	RedirectURI  string
	Scopes       []string
	CodeVerifier string
	CreatedAt    time.Time
}

type Store struct {
	mu    sync.Mutex
	items map[string]Pending
	ttl   time.Duration
}

func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Store{
		items: make(map[string]Pending),
		ttl:   ttl,
	}
}

func (s *Store) Put(state string, p Pending) {
	state = strings.TrimSpace(state)
	if state == "" || s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]Pending)
	}
	p.CreatedAt = time.Now()
	s.items[state] = p
}

func (s *Store) Pop(state string) (Pending, bool) {
	state = strings.TrimSpace(state)
	if state == "" || s == nil {
		return Pending{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[state]
	if ok {
		delete(s.items, state)
	}
	if !ok {
		return Pending{}, false
	}
	if s.ttl > 0 && !p.CreatedAt.IsZero() && time.Since(p.CreatedAt) > s.ttl {
		return Pending{}, false
	}
	return p, true
}

func NewPKCE() (verifier string, challenge string, err error) {
	// RFC 7636：verifier 建议 43-128 字符；这里用 32 字节随机数生成，得到 43 字符 base64url（无 padding）。
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	if len(verifier) < 43 {
		return "", "", errors.New("pkce: verifier too short")
	}
	return verifier, challenge, nil
}
