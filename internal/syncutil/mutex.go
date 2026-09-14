//go:build !deadlock

// Package syncutil re-exports Mutex and RWMutex so the rest of the
// codebase can declare lock fields without caring whether a build wants
// the plain runtime primitives (this file, the default) or the
// instrumented ones from mutex_deadlock.go (`go test -tags deadlock`,
// `go run -tags deadlock`).
//
// Every mutex-typed field in internal/ should use syncutil.Mutex /
// syncutil.RWMutex instead of sync.Mutex / sync.RWMutex so a `-tags
// deadlock` build/test run gets deadlock detection everywhere, not just in
// the one package that happened to need it. See mutex_deadlock.go for why:
// this exists because internal/agent/loop.go shipped a self-deadlock
// (appendToolResults held cfg.HistoryLock and called annotateToolCall,
// which locked the same non-reentrant mutex again) that hung every
// multi-tool-call turn for as long as the session ran, with no test
// catching it — every test that exercised that path used a nil lock, the
// one configuration where re-locking is a silent no-op instead of a hang.
package syncutil

import "sync"

// Mutex is sync.Mutex in normal builds.
type Mutex = sync.Mutex

// RWMutex is sync.RWMutex in normal builds.
type RWMutex = sync.RWMutex
