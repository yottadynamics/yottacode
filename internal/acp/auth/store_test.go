package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateAuthenticateAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := NewStoreAt(path)
	expires := time.Now().Add(time.Hour)
	record, raw, err := store.Create("laptop", []Scope{ScopeRead, ScopePrompt, ScopeRead}, []string{"/repo"}, expires)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if raw == "" || record.Hash == "" || record.ID == "" {
		t.Fatal("create did not return token metadata and one-time secret")
	}
	if got, err := store.Authenticate(raw, time.Now()); err != nil || got.ID != record.ID {
		t.Fatalf("authenticate = %+v, %v", got, err)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].Hash != "" {
		t.Fatalf("list = %+v, want one record without hash", listed)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token store mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreRejectsExpiredAndRevokedTokens(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "tokens.json"))
	record, raw, err := store.Create("test", []Scope{ScopeRead}, nil, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Authenticate(raw, record.ExpiresAt.Add(time.Second)); err != ErrExpiredToken {
		t.Fatalf("expired auth error = %v, want %v", err, ErrExpiredToken)
	}
	if err := store.Revoke(record.ID, time.Now()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := store.Authenticate(raw, time.Now()); err != ErrRevokedToken {
		t.Fatalf("revoked auth error = %v, want %v", err, ErrRevokedToken)
	}
}

func TestStoreRejectsUnknownScope(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "tokens.json"))
	if _, _, err := store.Create("bad", []Scope{"admin:all"}, nil, time.Time{}); err == nil {
		t.Fatal("expected unknown scope error")
	}
}
