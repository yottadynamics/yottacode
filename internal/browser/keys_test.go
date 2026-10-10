package browser

import (
	"testing"

	"github.com/chromedp/chromedp/kb"
)

func TestParseKeyToken(t *testing.T) {
	cases := map[string]string{
		"Enter":   kb.Enter,
		"enter":   kb.Enter,
		"Escape":  kb.Escape,
		"Esc":     kb.Escape,
		"Tab":     kb.Tab,
		"Control": kb.Control,
		"ctrl":    kb.Control,
		"Shift":   kb.Shift,
		"a":       kb.Keys['a'].Code,
		"A":       kb.Keys['A'].Code,
		"z":       kb.Keys['z'].Code,
		"1":       "1",
		"F1":      kb.F1,
	}
	for tok, want := range cases {
		got, err := parseKeyToken(tok)
		if err != nil {
			t.Errorf("parseKeyToken(%q): unexpected error: %v", tok, err)
			continue
		}
		if got != want {
			t.Errorf("parseKeyToken(%q) = %v, want %v", tok, got, want)
		}
	}
}

func TestParseKeyToken_Unrecognized(t *testing.T) {
	if _, err := parseKeyToken("not-a-real-key"); err == nil {
		t.Error("expected an error for an unrecognized key token")
	}
}

func TestParseHotkey_SingleKey(t *testing.T) {
	combo, err := parseHotkey("Enter")
	if err != nil {
		t.Fatalf("parseHotkey: %v", err)
	}
	if len(combo.modifiers) != 0 {
		t.Errorf("expected no modifiers, got %v", combo.modifiers)
	}
	if combo.main != kb.Enter {
		t.Errorf("main = %v, want Enter", combo.main)
	}
}

func TestParseHotkey_Combo(t *testing.T) {
	combo, err := parseHotkey("Control+a")
	if err != nil {
		t.Fatalf("parseHotkey: %v", err)
	}
	if len(combo.modifiers) != 1 || combo.modifiers[0] != kb.Control {
		t.Errorf("modifiers = %v, want [ControlLeft]", combo.modifiers)
	}
	if combo.main != kb.Keys['a'].Code {
		t.Errorf("main = %v, want KeyA", combo.main)
	}
}

func TestParseHotkey_MultiModifierCombo(t *testing.T) {
	combo, err := parseHotkey("Control+Shift+a")
	if err != nil {
		t.Fatalf("parseHotkey: %v", err)
	}
	if len(combo.modifiers) != 2 {
		t.Fatalf("modifiers = %v, want 2 entries", combo.modifiers)
	}
	if combo.modifiers[0] != kb.Control || combo.modifiers[1] != kb.Shift {
		t.Errorf("modifiers = %v, want [ControlLeft ShiftLeft]", combo.modifiers)
	}
	if combo.main != kb.Keys['a'].Code {
		t.Errorf("main = %v, want KeyA", combo.main)
	}
}

func TestParseHotkey_Empty(t *testing.T) {
	if _, err := parseHotkey(""); err == nil {
		t.Error("expected an error for an empty hotkey spec")
	}
	if _, err := parseHotkey("   "); err == nil {
		t.Error("expected an error for a blank hotkey spec")
	}
}

func TestParseHotkey_UnrecognizedToken(t *testing.T) {
	if _, err := parseHotkey("Control+nonsense"); err == nil {
		t.Error("expected an error for an unrecognized key in a combo")
	}
}
