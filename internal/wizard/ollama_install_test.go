package wizard

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestOllamaInstallCommand(t *testing.T) {
	tests := []struct {
		name, goos string
		brew       bool
		want       string
		wantErr    bool
	}{
		{name: "linux", goos: "linux", want: "curl -fsSL https://ollama.com/install.sh | sh"},
		{name: "macOS Homebrew", goos: "darwin", brew: true, want: "brew install ollama"},
		{name: "macOS official installer", goos: "darwin", want: "curl -fsSL https://ollama.com/install.sh | sh"},
		{name: "unsupported", goos: "windows", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OllamaInstallCommand(tt.goos, tt.brew)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v, want error=%v", err, tt.wantErr)
			}
			if err == nil && got.Display != tt.want {
				t.Fatalf("Display=%q, want %q", got.Display, tt.want)
			}
		})
	}
}

func TestViewEmbedSubStep_InstallPrompt(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 100
	m.installOllama = true
	m.ollamaInstall = OllamaInstallSpec{Display: "brew install ollama"}
	out := stripANSI(m.viewEmbedSubStep())
	for _, want := range []string{"Advanced semantic memory uses Ollama", "brew install ollama", "sudo password", "Enter/y install", "n/esc skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("install prompt missing %q; got:\n%s", want, out)
		}
	}
}

func TestViewEmbedSubStep_InstallFailureRecovery(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 100
	m.ollamaInstallErr = errors.New("installer exited with status 1")
	out := stripANSI(m.viewEmbedSubStep())
	for _, want := range []string{"Ollama setup", "ollama serve", "rerun setup"} {
		if !strings.Contains(out, want) {
			t.Errorf("recovery view missing %q; got:\n%s", want, out)
		}
	}
}

func TestUpdateEmbedSubStep_SkipInstall(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.installOllama = true
	updated, cmd := m.updateEmbedSubStep(tea.KeyPressMsg{Text: "n"})
	wm := updated.(wizardModel)
	if wm.installOllama || cmd != nil {
		t.Fatalf("skip should close install prompt without a command: install=%v cmd=%v", wm.installOllama, cmd != nil)
	}
}
