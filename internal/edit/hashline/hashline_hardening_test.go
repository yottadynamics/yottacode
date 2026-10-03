package hashline

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func kindOf(t *testing.T, err error) ErrorKind {
	t.Helper()
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("err = %v (%T), want *ApplyError", err, err)
	}
	return applyErr.Kind
}

// A hunk's Length is model-supplied. The bytes actually replaced must be
// exactly the bytes that were hashed, so Length has to agree with Old.
func TestApplyRejectsLengthThatDisagreesWithOld(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		old    string
		offset int
		length int
	}{
		// Longer than what is left of the file: used to panic in Apply.
		{"length past end of file", "aaa\nfoo\nbbb\n", "foo", 0, 100},
		// Found by relocation, then replaced Length bytes: silently deleted "bbb\n".
		{"length longer than old", "aaa\nfoo\nbbb\nccc\nddd\n", "foo", 0, 8},
		{"length shorter than old", "aaa\nfoo\nbbb\n", "foo", 4, 2},
		{"negative length", "aaa\nfoo\n", "foo", 4, -3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Apply panicked instead of returning an error: %v", r)
				}
			}()
			src := []byte(c.src)
			hunk := Hunk{
				Anchor: Anchor{Offset: c.offset, Length: c.length, Hash: hashBytes([]byte(c.old))},
				Old:    []byte(c.old),
				New:    []byte("X"),
			}
			out, err := Apply(src, []Hunk{hunk})
			if err == nil {
				t.Fatalf("Apply succeeded and produced %q; a length that disagrees with old must be rejected", out)
			}
			if got := kindOf(t, err); got != ErrInvalidRange {
				t.Errorf("kind = %q, want %q", got, ErrInvalidRange)
			}
			if out != nil {
				t.Errorf("out = %q, want nil on error", out)
			}
			if string(src) != c.src {
				t.Errorf("Apply modified its input: %q", src)
			}
		})
	}
}

// An empty span hashes to a constant, so an insert anchored on it verifies
// nothing about the file: if the file changed, the text lands wherever the
// stale offset now points, even inside a multi-byte character.
func TestApplyRejectsEmptyOld(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		offset int
	}{
		{"stale offset inside a word", "HEADER\nalpha\nbeta\n", 6},
		{"offset inside a multi-byte rune", "héllo\n", 2},
		{"empty file", "", 0},
		{"end of file", "one\n", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hunk := Hunk{Anchor: Anchor{Offset: c.offset, Length: 0, Hash: hashBytes(nil)}, New: []byte("INSERTED\n")}
			out, err := Apply([]byte(c.src), []Hunk{hunk})
			if err == nil {
				t.Fatalf("Apply succeeded with an unanchored insert: %q", out)
			}
			if got := kindOf(t, err); got != ErrEmptyAnchor {
				t.Errorf("kind = %q, want %q", got, ErrEmptyAnchor)
			}
			if !strings.Contains(err.Error(), "adjacent") {
				t.Errorf("error should say how to express an insert (anchor on adjacent text): %v", err)
			}
		})
	}
}

// The supported way to insert: anchor on the neighbouring text and replace it
// with itself plus the addition.
func TestApplyInsertViaAdjacentReplace(t *testing.T) {
	cases := []struct {
		name, src, old, new, want string
	}{
		{"after a line", "one\ntwo\n", "one\n", "one\ninserted\n", "one\ninserted\ntwo\n"},
		{"before a line", "one\ntwo\n", "two\n", "inserted\ntwo\n", "one\ninserted\ntwo\n"},
		{"unicode neighbour", "héllo\nwörld\n", "héllo\n", "héllo\n日本語\n", "héllo\n日本語\nwörld\n"},
		{"crlf neighbour", "a\r\nb\r\n", "a\r\n", "a\r\nX\r\n", "a\r\nX\r\nb\r\n"},
		{"no trailing newline at eof", "a\nb", "b", "b\nc", "a\nb\nc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := []byte(c.src)
			off := strings.Index(c.src, c.old)
			anchor := mustHashSpan(t, src, off, len(c.old))
			out, err := Apply(src, []Hunk{{Anchor: anchor, Old: []byte(c.old), New: []byte(c.new)}})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("out = %q, want %q", out, c.want)
			}
			if !utf8.Valid(out) {
				t.Errorf("output is not valid UTF-8: %q", out)
			}
		})
	}
}

// Apply is byte-exact by design: line-ending differences are real changes.
func TestHashSpanIsByteExact(t *testing.T) {
	hash := func(s string) string {
		a, err := HashSpan([]byte(s), 0, len(s))
		if err != nil {
			t.Fatal(err)
		}
		return a.Hash
	}
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"identical", "a\nb\n", "a\nb\n", true},
		{"crlf differs from lf", "a\r\nb\r\n", "a\nb\n", false},
		{"trailing newline matters", "a\nb", "a\nb\n", false},
		{"unicode normalization forms differ", "é", "é", false},
		{"one changed rune", "日本語", "日本誤", false},
		{"whitespace matters", "a b", "a  b", false},
	}
	for _, c := range cases {
		if got := hash(c.a) == hash(c.b); got != c.same {
			t.Errorf("%s: hash equality = %v, want %v", c.name, got, c.same)
		}
	}
	// Fixed vectors keep the truncated SHA-256 stable across platforms.
	if got, want := hash(""), "e3b0c44298fc1c14"; got != want {
		t.Errorf("hash(empty) = %s, want %s", got, want)
	}
	if got, want := hash("abc"), "ba7816bf8f01cfea"; got != want {
		t.Errorf("hash(abc) = %s, want %s", got, want)
	}
}

func TestHashMismatchMessageDoesNotAskModelToComputeHashes(t *testing.T) {
	src := []byte("alpha\nbeta\n")
	_, err := Apply(src, []Hunk{{Anchor: Anchor{Offset: 6, Length: 4, Hash: "0000000000000000"}, Old: []byte("beta"), New: []byte("B")}})
	if err == nil {
		t.Fatal("want a hash mismatch")
	}
	if strings.Contains(err.Error(), "recompute") {
		t.Errorf("a model cannot compute SHA-256; the message must tell it to re-read instead: %v", err)
	}
	if !strings.Contains(err.Error(), "old bytes do not match anchor hash") || !strings.Contains(err.Error(), "re-read") {
		t.Errorf("message lost its recovery guidance: %v", err)
	}
}
