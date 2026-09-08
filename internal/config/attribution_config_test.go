package config

import (
	"strings"
	"testing"
)

func TestAttributionConfig_ResolvesDefaultsAndOptOut(t *testing.T) {
	tests := []struct {
		name        string
		cfg         AttributionConfig
		wantTrailer string
		wantFooter  string
	}{
		{
			name:        "absent block uses defaults",
			wantTrailer: DefaultCommitAttributionTrailer,
			wantFooter:  DefaultPRAttributionFooter,
		},
		{
			name: "disabled removes both surfaces",
			cfg: AttributionConfig{
				Disabled: true,
				Trailer:  "Co-authored-by: custom <custom@example.com>",
			},
		},
		{
			name: "override changes commit only",
			cfg: AttributionConfig{
				Trailer: "Co-authored-by: custom <custom@example.com>",
			},
			wantTrailer: "Co-authored-by: custom <custom@example.com>",
			wantFooter:  DefaultPRAttributionFooter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.CommitTrailer(); got != tt.wantTrailer {
				t.Fatalf("CommitTrailer() = %q, want %q", got, tt.wantTrailer)
			}
			if got := tt.cfg.PRFooter(); got != tt.wantFooter {
				t.Fatalf("PRFooter() = %q, want %q", got, tt.wantFooter)
			}
		})
	}
}

func TestLoad_AttributionRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, `
[attribution]
disabled = true
trailer = "Co-authored-by: custom <custom@example.com>"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Attribution.Disabled {
		t.Fatal("attribution.disabled = false, want true")
	}
	if cfg.Attribution.Trailer != "Co-authored-by: custom <custom@example.com>" {
		t.Fatalf("attribution.trailer = %q", cfg.Attribution.Trailer)
	}

	rendered := Render(cfg)
	if !strings.Contains(rendered, "[attribution]") {
		t.Fatalf("Render dropped [attribution]:\n%s", rendered)
	}
	roundTripped, err := Load(writeFile(t, rendered))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if roundTripped.Attribution != cfg.Attribution {
		t.Fatalf("attribution changed after round trip: got %+v, want %+v", roundTripped.Attribution, cfg.Attribution)
	}
}
