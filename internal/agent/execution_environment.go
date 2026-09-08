package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yottadynamics/yottacode/internal/sandboxcache"
)

// executionEnvironment is the single policy used by command tools: HOME,
// XDG, and temporary state are always redirected to yottacode-owned paths
// outside the workspace. Go-specific paths follow the same rule and telemetry
// is disabled. Sandboxes use /var/tmp (never their noexec /tmp or repo-local
// caches); host commands use a workspace-safe host scratch root.
type executionEnvironment struct {
	home, tmp                    string
	xdgCache, xdgConfig, xdgData string
	xdgState                     string
	goTmp, goCache, goModCache   string
}

func commandEnvironment(root string, sandboxed, withGo bool) (executionEnvironment, error) {
	var home string
	var err error
	if sandboxed {
		home = filepath.ToSlash(filepath.Join("/var/tmp", "yottacode-go", safeScratchName(root)))
	} else {
		home, err = sandboxcache.HostShellScratchDir(root)
		if err != nil {
			return executionEnvironment{}, err
		}
	}
	e := executionEnvironment{
		home: home, tmp: filepath.Join(home, "tmp"),
		xdgCache: filepath.Join(home, "xdg-cache"), xdgConfig: filepath.Join(home, "xdg-config"),
		xdgData: filepath.Join(home, "xdg-data"), xdgState: filepath.Join(home, "xdg-state"),
	}
	if withGo {
		e.goTmp = e.tmp
		if sandboxed {
			cacheRoot, cacheErr := sandboxcache.GoHostCacheDirForWorkspace(root)
			if cacheErr != nil {
				return executionEnvironment{}, cacheErr
			}
			e.goCache, e.goModCache = filepath.Join(cacheRoot, "cache"), filepath.Join(cacheRoot, "modcache")
		} else {
			goRoot, cacheErr := sandboxcache.HostGoScratchDir(root)
			if cacheErr != nil {
				return executionEnvironment{}, cacheErr
			}
			e.home = goRoot
			e.tmp, e.goTmp = filepath.Join(goRoot, "tmp"), filepath.Join(goRoot, "tmp")
			e.xdgCache, e.xdgConfig = filepath.Join(goRoot, "xdg-cache"), filepath.Join(goRoot, "xdg-config")
			e.xdgData, e.xdgState = filepath.Join(goRoot, "xdg-data"), filepath.Join(goRoot, "xdg-state")
			e.goCache, e.goModCache = filepath.Join(goRoot, "cache"), filepath.Join(goRoot, "modcache")
		}
	}
	return e, nil
}

func (e executionEnvironment) wrap(command string, withGo bool) string {
	dirs := []string{e.tmp, e.xdgCache, e.xdgConfig, e.xdgData, e.xdgState}
	if withGo {
		dirs = []string{e.goTmp, e.goCache, e.goModCache, e.xdgCache, e.xdgConfig, e.xdgData, e.xdgState}
	}
	assignments := []string{
		"HOME=" + shellQuoteSingle(e.home), "TMPDIR=" + shellQuoteSingle(e.tmp),
		"XDG_CACHE_HOME=" + shellQuoteSingle(e.xdgCache), "XDG_CONFIG_HOME=" + shellQuoteSingle(e.xdgConfig),
		"XDG_DATA_HOME=" + shellQuoteSingle(e.xdgData), "XDG_STATE_HOME=" + shellQuoteSingle(e.xdgState),
	}
	if withGo {
		dirs = append(dirs, e.goTmp, e.goCache, e.goModCache)
		assignments = append(assignments, "GOTMPDIR="+shellQuoteSingle(e.goTmp), "GOCACHE="+shellQuoteSingle(e.goCache), "GOMODCACHE="+shellQuoteSingle(e.goModCache), "GOTELEMETRY='off'")
	}
	quoted := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir != "" && !seen[dir] {
			seen[dir] = true
			quoted = append(quoted, shellQuoteSingle(dir))
		}
	}
	return fmt.Sprintf("mkdir -p %s && export %s && %s", strings.Join(quoted, " "), strings.Join(assignments, " "), command)
}
