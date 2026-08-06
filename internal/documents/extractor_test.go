package documents

import "testing"

func TestExtractRequestWithDefaultsFillsZeroValues(t *testing.T) {
	got := ExtractRequest{}.withDefaults()

	if got.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", got.MaxBytes, DefaultMaxBytes)
	}
	if got.MaxRows != DefaultMaxRows {
		t.Errorf("MaxRows = %d, want %d", got.MaxRows, DefaultMaxRows)
	}
	if got.MaxChars != DefaultMaxChars {
		t.Errorf("MaxChars = %d, want %d", got.MaxChars, DefaultMaxChars)
	}
}

// TestExtractRequestWithDefaultsClampsMaxBytes: MaxBytes is the cap that
// bounds all the others (no extractor can sample more rows or characters
// than the bytes it was allowed to read), so it's the one that needs a
// ceiling — and the clamp lives in withDefaults rather than at the tool
// boundary so any future caller inherits it too.
func TestExtractRequestWithDefaultsClampsMaxBytes(t *testing.T) {
	got := ExtractRequest{MaxBytes: MaxAllowedBytes * 10}.withDefaults()

	if got.MaxBytes != MaxAllowedBytes {
		t.Errorf("MaxBytes = %d, want it clamped to %d", got.MaxBytes, MaxAllowedBytes)
	}
}

func TestExtractRequestWithDefaultsKeepsValidOverrides(t *testing.T) {
	in := ExtractRequest{MaxBytes: 4096, MaxRows: 7, MaxChars: 99}
	got := in.withDefaults()

	if got.MaxBytes != 4096 || got.MaxRows != 7 || got.MaxChars != 99 {
		t.Errorf("withDefaults altered valid overrides: %+v -> %+v", in, got)
	}
}
