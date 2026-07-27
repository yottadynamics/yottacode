package codemap

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

// Provider returns the latest code-map snapshot. TUI rebuilds can swap the
// snapshot while agent tools keep depending on this tiny read-only surface.
type Provider interface {
	Index(ctx context.Context) (*CodeIndex, error)
}

// StaticProvider returns one immutable index, useful in tests and one-shot runs.
type StaticProvider struct{ Snapshot *CodeIndex }

func (p StaticProvider) Index(context.Context) (*CodeIndex, error) { return p.Snapshot, nil }

// BuilderProvider rebuilds the index on demand.
type BuilderProvider struct{ Options BuildOptions }

func (p BuilderProvider) Index(ctx context.Context) (*CodeIndex, error) { return Build(ctx, p.Options) }

// CachedProvider reuses the last index until supported source file paths,
// mtimes, sizes, or counts change. This is not a file watcher yet, but it avoids
// rebuilding the map on every /map render or agent query while still reflecting
// normal edits on the next request.
type CachedProvider struct {
	Options BuildOptions

	mu          sync.Mutex
	snapshot    *CodeIndex
	fingerprint workspaceFingerprint
}

func (p *CachedProvider) Index(ctx context.Context) (*CodeIndex, error) {
	fp, err := fingerprintWorkspace(ctx, p.Options.Root, p.Options.MaxFiles)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot != nil && p.fingerprint == fp {
		return p.snapshot, nil
	}
	idx, err := Build(ctx, p.Options)
	if err != nil {
		return nil, err
	}
	p.snapshot = idx
	p.fingerprint = fp
	return idx, nil
}

type workspaceFingerprint struct {
	Files     int
	LatestMod int64
	TotalSize int64
}

func fingerprintWorkspace(ctx context.Context, root string, maxFiles int) (workspaceFingerprint, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return workspaceFingerprint{}, err
	}
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	var fp workspaceFingerprint
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == absRoot {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := lsp.ResolveFile(path); !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fp.Files++
		fp.TotalSize += info.Size()
		if mod := info.ModTime().UTC().Round(time.Nanosecond).UnixNano(); mod > fp.LatestMod {
			fp.LatestMod = mod
		}
		if fp.Files > maxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return workspaceFingerprint{}, err
	}
	return fp, nil
}
