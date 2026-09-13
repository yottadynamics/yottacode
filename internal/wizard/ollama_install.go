package wizard

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OllamaInstallSpec contains argv for an installer and the exact command shown
// to the user before execution. Keeping argv separate avoids shell expansion
// for the Homebrew path while retaining the official installer for Linux.
type OllamaInstallSpec struct {
	Command string
	Args    []string
	Display string
}

// OllamaInstallCommand returns the supported platform-specific installer.
// Unsupported platforms receive an actionable error instead of a guessed
// command.
func OllamaInstallCommand(goos string, homebrewAvailable bool) (OllamaInstallSpec, error) {
	const official = "curl -fsSL https://ollama.com/install.sh | sh"
	switch goos {
	case "linux":
		return OllamaInstallSpec{"sh", []string{"-c", official}, official}, nil
	case "darwin":
		if homebrewAvailable {
			return OllamaInstallSpec{"brew", []string{"install", "ollama"}, "brew install ollama"}, nil
		}
		return OllamaInstallSpec{"sh", []string{"-c", official}, official}, nil
	default:
		return OllamaInstallSpec{}, fmt.Errorf("automatic Ollama installation is not supported on %s", goos)
	}
}

// DetectOllamaInstallCommand chooses Homebrew on macOS when available and the
// official installer everywhere else in the supported platform set.
func DetectOllamaInstallCommand() (OllamaInstallSpec, error) {
	_, brewErr := exec.LookPath("brew")
	return OllamaInstallCommand(runtime.GOOS, brewErr == nil)
}
