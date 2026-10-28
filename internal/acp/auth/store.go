package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Scope controls what an authenticated remote ACP client may do. Authorization
// enforcement beyond transport admission is added by the ACP server layer.
type Scope string

const (
	ScopeRead    Scope = "read"
	ScopePrompt  Scope = "prompt"
	ScopeApprove Scope = "approve"
	ScopeAdmin   Scope = "admin"
)

var validScopes = map[Scope]bool{
	ScopeRead: true, ScopePrompt: true, ScopeApprove: true, ScopeAdmin: true,
}

var ErrInvalidToken = errors.New("invalid ACP token")
var ErrExpiredToken = errors.New("expired ACP token")
var ErrRevokedToken = errors.New("revoked ACP token")

// TokenRecord is the persisted metadata for one bearer token. The raw secret
// is intentionally never stored or returned after creation.
type TokenRecord struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Salt         string    `json:"salt"`
	Hash         string    `json:"hash"`
	Scopes       []Scope   `json:"scopes"`
	ProjectRoots []string  `json:"project_roots,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	LastUsedAt   time.Time `json:"last_used_at,omitempty"`
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
}

// Store is a small atomic JSON token store suitable for the single daemon
// owner. The file is user-only (0600); the containing directory is 0700.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns the default ~/.yottacode/acp-tokens.json store.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	return &Store{path: filepath.Join(home, ".yottacode", "acp-tokens.json")}, nil
}

// NewStoreAt is used by tests and isolated deployments.
func NewStoreAt(path string) *Store { return &Store{path: path} }

// Create generates a 256-bit opaque bearer token and persists only its hash.
func (s *Store) Create(name string, scopes []Scope, projectRoots []string, expiresAt time.Time) (TokenRecord, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TokenRecord{}, "", errors.New("token name is required")
	}
	scopes, err := normalizeScopes(scopes)
	if err != nil {
		return TokenRecord{}, "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return TokenRecord{}, "", fmt.Errorf("generate token: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return TokenRecord{}, "", fmt.Errorf("generate token salt: %w", err)
	}
	raw := "ytc_" + base64.RawURLEncoding.EncodeToString(secret)
	record := TokenRecord{
		ID:           base64.RawURLEncoding.EncodeToString(secret[:8]),
		Name:         name,
		Salt:         base64.RawURLEncoding.EncodeToString(salt),
		Hash:         hash(salt, raw),
		Scopes:       scopes,
		ProjectRoots: cleanRoots(projectRoots),
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
	}
	if err := s.update(func(records *[]TokenRecord) error {
		*records = append(*records, record)
		return nil
	}); err != nil {
		return TokenRecord{}, "", err
	}
	return record, raw, nil
}

// Authenticate validates a raw bearer token and records last use. The caller
// receives a copy of metadata, never the secret or mutable store state.
func (s *Store) Authenticate(raw string, now time.Time) (TokenRecord, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TokenRecord{}, ErrInvalidToken
	}
	var found TokenRecord
	err := s.update(func(records *[]TokenRecord) error {
		for i := range *records {
			r := &(*records)[i]
			if subtle.ConstantTimeCompare([]byte(r.Hash), []byte(hashString(r.Salt, raw))) != 1 {
				continue
			}
			if !r.RevokedAt.IsZero() {
				return ErrRevokedToken
			}
			if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
				return ErrExpiredToken
			}
			r.LastUsedAt = now.UTC()
			found = *r
			found.Scopes = append([]Scope(nil), r.Scopes...)
			found.ProjectRoots = append([]string(nil), r.ProjectRoots...)
			return nil
		}
		return ErrInvalidToken
	})
	return found, err
}

// List returns token metadata without hashes.
func (s *Store) List() ([]TokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Hash = ""
	}
	return records, nil
}

// Revoke marks a token unusable without deleting its audit metadata.
func (s *Store) Revoke(id string, now time.Time) error {
	return s.update(func(records *[]TokenRecord) error {
		for i := range *records {
			if (*records)[i].ID == id {
				(*records)[i].RevokedAt = now.UTC()
				return nil
			}
		}
		return fmt.Errorf("token %q not found", id)
	})
}

// Rotate revokes an existing token and creates a replacement with the same
// name, scopes, project roots, and expiration. The replacement secret is
// returned once; the old token becomes invalid before this call returns.
func (s *Store) Rotate(id string, now time.Time) (TokenRecord, string, error) {
	var previous TokenRecord
	if err := s.withRecords(func(records *[]TokenRecord) error {
		for i := range *records {
			if (*records)[i].ID == id {
				previous = (*records)[i]
				if !previous.RevokedAt.IsZero() {
					return ErrRevokedToken
				}
				(*records)[i].RevokedAt = now.UTC()
				return nil
			}
		}
		return fmt.Errorf("token %q not found", id)
	}); err != nil {
		return TokenRecord{}, "", err
	}
	return s.Create(previous.Name, previous.Scopes, previous.ProjectRoots, previous.ExpiresAt)
}

func (s *Store) update(fn func(*[]TokenRecord) error) error {
	return s.withRecords(fn)
}

func (s *Store) withRecords(fn func(*[]TokenRecord) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fn(&records); err != nil {
		return err
	}
	return s.saveLocked(records)
}

func (s *Store) loadLocked() ([]TokenRecord, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read token store: %w", err)
	}
	var records []TokenRecord
	if err := jsonUnmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("parse token store: %w", err)
	}
	return records, nil
}

func (s *Store) saveLocked(records []TokenRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create token store directory: %w", err)
	}
	b, err := jsonMarshal(records)
	if err != nil {
		return fmt.Errorf("encode token store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".acp-tokens-*.tmp")
	if err != nil {
		return fmt.Errorf("create token store temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace token store: %w", err)
	}
	return nil
}

func normalizeScopes(scopes []Scope) ([]Scope, error) {
	if len(scopes) == 0 {
		return nil, errors.New("at least one token scope is required")
	}
	seen := map[Scope]bool{}
	out := make([]Scope, 0, len(scopes))
	for _, scope := range scopes {
		if !validScopes[scope] {
			return nil, fmt.Errorf("unknown token scope %q", scope)
		}
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out, nil
}

func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root = strings.TrimSpace(root); root != "" {
			out = append(out, filepath.Clean(root))
		}
	}
	return out
}

func hash(salt []byte, raw string) string {
	input := append(append([]byte(nil), salt...), raw...)
	sum := sha256.Sum256(input)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func hashString(encodedSalt, raw string) string {
	salt, err := base64.RawURLEncoding.DecodeString(encodedSalt)
	if err != nil {
		return ""
	}
	return hash(salt, raw)
}

var jsonMarshal = func(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
var jsonUnmarshal = json.Unmarshal
