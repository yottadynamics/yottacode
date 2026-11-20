package tui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/filerefs"
)

// writeShot creates a screenshot-like PNG with spaces in its name in a
// directory outside the model's cwd, mirroring ~/Pictures/Screenshots.
func writeShot(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Screenshot from 2026-11-20 10-18-05.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartTurn_AtImageOutsideCwdIsAttached(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = t.TempDir()
	m.providerProfile.SupportsImages = true
	shot := writeShot(t)

	out, _ := m.startTurn(`what is wrong in @"` + shot + `" ?`)
	m2 := out.(Model)
	defer m2.turnCancel()

	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if len(last.Images) != 1 || last.Images[0].MediaType != "image/png" {
		t.Fatalf("images on user message = %+v", last.Images)
	}
	if text := filerefs.StripAttachment(last.Content); strings.Contains(text, "@") {
		t.Fatalf("@ should be stripped from an attached ref: %q", text)
	}
	if got := m2.transcript.String(); !strings.Contains(got, "attached") {
		t.Fatalf("no attached notice in transcript: %q", got)
	}
}

func TestStartTurn_AtImageRejectedWhenModelLacksVision(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Adapter = stubAdapterNoStream{}
	m.cwd = t.TempDir()
	m.providerProfile.SupportsImages = false
	shot := writeShot(t)

	out, _ := m.startTurn(`look at @"` + shot + `"`)
	m2 := out.(Model)
	defer m2.turnCancel()

	last := m2.sess.Messages[len(m2.sess.Messages)-1]
	if len(last.Images) != 0 {
		t.Fatalf("images must not be attached: %+v", last.Images)
	}
	if got := m2.transcript.String(); !strings.Contains(got, "does not support image input") {
		t.Fatalf("expected a visible rejection notice, got %q", got)
	}
}
