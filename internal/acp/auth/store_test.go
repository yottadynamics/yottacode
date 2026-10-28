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

func TestTokenRecordAllowsProject(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	record := TokenRecord{ProjectRoots: []string{root}}
	if !record.AllowsProject(root) || !record.AllowsProject(nested) {
		t.Fatal("expected project root and descendants to be allowed")
	}
	if record.AllowsProject(t.TempDir()) {
		t.Fatal("unexpected project outside allowed root")
	}
	if (TokenRecord{}).AllowsProject(t.TempDir()) == false {
		t.Fatal("unpinned token should allow all projects")
	}
}

func TestStoreRotateRevokesOldToken(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "tokens.json"))
	old, raw, err := store.Create("laptop", []Scope{ScopeRead}, nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	replacement, next, err := store.Rotate(old.ID, time.Now())
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if replacement.ID == old.ID || next == raw {
		t.Fatal("rotation did not create a new secret")
	}
	if _, err := store.Authenticate(raw, time.Now()); err != ErrRevokedToken {
		t.Fatalf("old token error = %v, want %v", err, ErrRevokedToken)
	}
	if got, err := store.Authenticate(next, time.Now()); err != nil || got.Name != "laptop" {
		t.Fatalf("new token = %+v, %v", got, err)
	}
}

func TestStoreRejectsUnknownScope(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "tokens.json"))
	if _, _, err := store.Create("bad", []Scope{"admin:all"}, nil, time.Time{}); err == nil {
		t.Fatal("expected unknown scope error")
	}
}
