package browser

import (
	"testing"

	"github.com/go-rod/rod/lib/input"
)

func TestParseKeyToken(t *testing.T) {
	cases := map[string]input.Key{
		"Enter":   input.Enter,
		"enter":   input.Enter,
		"Escape":  input.Escape,
		"Esc":     input.Escape,
		"Tab":     input.Tab,
		"Control": input.ControlLeft,
		"ctrl":    input.ControlLeft,
		"Shift":   input.ShiftLeft,
		"a":       input.KeyA,
		"A":       input.KeyA,
		"z":       input.KeyZ,
		"1":       input.Digit1,
		"F1":      input.F1,
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
	if combo.main != input.Enter {
		t.Errorf("main = %v, want Enter", combo.main)
	}
}

func TestParseHotkey_Combo(t *testing.T) {
	combo, err := parseHotkey("Control+a")
	if err != nil {
		t.Fatalf("parseHotkey: %v", err)
	}
	if len(combo.modifiers) != 1 || combo.modifiers[0] != input.ControlLeft {
		t.Errorf("modifiers = %v, want [ControlLeft]", combo.modifiers)
	}
	if combo.main != input.KeyA {
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
	if combo.modifiers[0] != input.ControlLeft || combo.modifiers[1] != input.ShiftLeft {
		t.Errorf("modifiers = %v, want [ControlLeft ShiftLeft]", combo.modifiers)
	}
	if combo.main != input.KeyA {
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
