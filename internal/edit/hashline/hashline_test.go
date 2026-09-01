package hashline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashSpanReturnsTruncatedSHA256Anchor(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	anchor, err := HashSpan(src, 6, 4)
	if err != nil {
		t.Fatalf("HashSpan: %v", err)
	}
	if anchor.Offset != 6 || anchor.Length != 4 {
		t.Fatalf("anchor range = %d:%d, want 6:4", anchor.Offset, anchor.Length)
	}
	if anchor.Hash != "f44e64e75f3948e9" {
		t.Fatalf("anchor hash = %q, want truncated beta hash", anchor.Hash)
	}
}

func TestApplyExactReplacement(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	anchor := mustHashSpan(t, src, 6, 4)

	out, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte("beta"), New: []byte("BETA")}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := string(out), "alpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestApplyInsertionDeletionAndReplacement(t *testing.T) {
	t.Run("insertion", func(t *testing.T) {
		src := []byte("one\ntwo\n")
		anchor := mustHashSpan(t, src, 4, 0)
		out, err := Apply(src, []Hunk{{Anchor: anchor, Old: nil, New: []byte("inserted\n")}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, want := string(out), "one\ninserted\ntwo\n"; got != want {
			t.Fatalf("out = %q, want %q", got, want)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		src := []byte("one\ntwo\n")
		anchor := mustHashSpan(t, src, 4, 4)
		out, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte("two\n"), New: nil}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, want := string(out), "one\n"; got != want {
			t.Fatalf("out = %q, want %q", got, want)
		}
	})

	t.Run("replacement", func(t *testing.T) {
		src := []byte("one\ntwo\n")
		anchor := mustHashSpan(t, src, 4, 3)
		out, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte("two"), New: []byte("TWO")}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, want := string(out), "one\nTWO\n"; got != want {
			t.Fatalf("out = %q, want %q", got, want)
		}
	})
}

func TestApplyRejectsOverlappingHunks(t *testing.T) {
	src := []byte("abcdef\n")
	first := mustHashSpan(t, src, 1, 3)
	second := mustHashSpan(t, src, 3, 2)

	_, err := Apply(src, []Hunk{
		{Anchor: first, Old: []byte("bcd"), New: []byte("BCD")},
		{Anchor: second, Old: []byte("de"), New: []byte("DE")},
	})
	if !errorKindIs(err, ErrOverlap) {
		t.Fatalf("Apply error = %v, want ErrOverlap", err)
	}
}

func TestApplyRelocatesUniqueStaleOffset(t *testing.T) {
	original := []byte("alpha\nbeta\ngamma\n")
	current := []byte("header\nalpha\nbeta\ngamma\n")
	anchor := mustHashSpan(t, original, 6, 4)

	out, err := Apply(current, []Hunk{{Anchor: anchor, Old: []byte("beta"), New: []byte("BETA")}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := string(out), "header\nalpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestApplyRejectsAmbiguousRelocatedAnchorWithoutWriting(t *testing.T) {
	original := []byte("one\ntarget\nthree\n")
	current := []byte("target\none\ntarget\nthree\n")
	anchor := mustHashSpan(t, original, 4, 6)

	_, err := Apply(current, []Hunk{{Anchor: anchor, Old: []byte("target"), New: []byte("TARGET")}})
	if !errorKindIs(err, ErrAmbiguousAnchor) {
		t.Fatalf("Apply error = %v, want ErrAmbiguousAnchor", err)
	}
}

func TestApplyReportsStaleAnchorWithSuggestedReadRange(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	anchor := Anchor{Offset: 6, Length: 4, Hash: "0000000000000000"}

	_, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte("beta"), New: []byte("BETA")}})
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("Apply error = %T %[1]v, want *ApplyError", err)
	}
	if applyErr.Kind != ErrStaleAnchor {
		t.Fatalf("error kind = %q, want %q", applyErr.Kind, ErrStaleAnchor)
	}
	if applyErr.ExpectedHash != "0000000000000000" || applyErr.FoundHash == "" {
		t.Fatalf("hash context = expected %q found %q", applyErr.ExpectedHash, applyErr.FoundHash)
	}
	if applyErr.RereadStart >= applyErr.RereadEnd {
		t.Fatalf("reread range = %d:%d, want non-empty", applyErr.RereadStart, applyErr.RereadEnd)
	}
}

func TestApplyPreservesTrailingNewline(t *testing.T) {
	src := []byte("one\ntwo\n")
	anchor := mustHashSpan(t, src, 4, 3)

	out, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte("two"), New: []byte("TWO")}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := string(out), "one\nTWO\n"; got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestApplyFileRejectsInvalidUTF8AndBinary(t *testing.T) {
	tmp := t.TempDir()
	for name, content := range map[string][]byte{
		"invalid.txt": {0xff, 0xfe, 0xfd},
		"binary.txt":  []byte("abc\x00def"),
	} {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		err := ApplyFile(path, []Hunk{{Anchor: Anchor{Offset: 0, Length: 0, Hash: "e3b0c44298fc1c14"}, Old: nil, New: []byte("x")}})
		if !errorKindIs(err, ErrInvalidText) {
			t.Fatalf("ApplyFile(%s) error = %v, want ErrInvalidText", name, err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile: %v", readErr)
		}
		if string(got) != string(content) {
			t.Fatalf("ApplyFile(%s) modified rejected file", name)
		}
	}
}

func TestApplyFileWritesAtomicallyOnSuccess(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	anchor := mustHashSpan(t, []byte("alpha\nbeta\n"), 6, 4)

	if err := ApplyFile(path, []Hunk{{Anchor: anchor, Old: []byte("beta"), New: []byte("BETA")}}); err != nil {
		t.Fatalf("ApplyFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "alpha\nBETA\n" {
		t.Fatalf("file content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".file.txt.") {
			t.Fatalf("temporary file was not cleaned up: %s", entry.Name())
		}
	}
}

func mustHashSpan(t *testing.T, src []byte, offset, length int) Anchor {
	t.Helper()
	anchor, err := HashSpan(src, offset, length)
	if err != nil {
		t.Fatalf("HashSpan(%d, %d): %v", offset, length, err)
	}
	return anchor
}

func errorKindIs(err error, kind ErrorKind) bool {
	var applyErr *ApplyError
	return errors.As(err, &applyErr) && applyErr.Kind == kind
}
