package browser

import (
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/input"
)

// namedKeys covers the non-alphanumeric keys browser_hotkey callers name by
// their common label (e.g. "Enter", "Escape", "ArrowDown").
var namedKeys = map[string]input.Key{
	"enter":      input.Enter,
	"return":     input.Enter,
	"escape":     input.Escape,
	"esc":        input.Escape,
	"tab":        input.Tab,
	"backspace":  input.Backspace,
	"delete":     input.Delete,
	"del":        input.Delete,
	"space":      input.Space,
	"up":         input.ArrowUp,
	"arrowup":    input.ArrowUp,
	"down":       input.ArrowDown,
	"arrowdown":  input.ArrowDown,
	"left":       input.ArrowLeft,
	"arrowleft":  input.ArrowLeft,
	"right":      input.ArrowRight,
	"arrowright": input.ArrowRight,
	"home":       input.Home,
	"end":        input.End,
	"pageup":     input.PageUp,
	"pagedown":   input.PageDown,
	"f1":         input.F1,
	"f2":         input.F2,
	"f3":         input.F3,
	"f4":         input.F4,
	"f5":         input.F5,
	"f6":         input.F6,
	"f7":         input.F7,
	"f8":         input.F8,
	"f9":         input.F9,
	"f10":        input.F10,
	"f11":        input.F11,
	"f12":        input.F12,
}

// modifierKeys covers the tokens that may prefix a combo (e.g. the
// "Control" in "Control+a"). Only the left variant is used — combos don't
// need to distinguish left/right modifiers.
var modifierKeys = map[string]input.Key{
	"control": input.ControlLeft,
	"ctrl":    input.ControlLeft,
	"shift":   input.ShiftLeft,
	"alt":     input.AltLeft,
	"option":  input.AltLeft,
	"meta":    input.MetaLeft,
	"cmd":     input.MetaLeft,
	"command": input.MetaLeft,
	"super":   input.MetaLeft,
}

var digitKeys = map[byte]input.Key{
	'0': input.Digit0, '1': input.Digit1, '2': input.Digit2, '3': input.Digit3, '4': input.Digit4,
	'5': input.Digit5, '6': input.Digit6, '7': input.Digit7, '8': input.Digit8, '9': input.Digit9,
}

var letterKeys = map[byte]input.Key{
	'a': input.KeyA, 'b': input.KeyB, 'c': input.KeyC, 'd': input.KeyD, 'e': input.KeyE,
	'f': input.KeyF, 'g': input.KeyG, 'h': input.KeyH, 'i': input.KeyI, 'j': input.KeyJ,
	'k': input.KeyK, 'l': input.KeyL, 'm': input.KeyM, 'n': input.KeyN, 'o': input.KeyO,
	'p': input.KeyP, 'q': input.KeyQ, 'r': input.KeyR, 's': input.KeyS, 't': input.KeyT,
	'u': input.KeyU, 'v': input.KeyV, 'w': input.KeyW, 'x': input.KeyX, 'y': input.KeyY,
	'z': input.KeyZ,
}

// parseKeyToken resolves one "+"-separated component of a hotkey spec
// (e.g. "Control", "a", "Enter") to its rod input.Key. Matching is
// case-insensitive; single letters/digits map to the corresponding
// physical key.
func parseKeyToken(tok string) (input.Key, error) {
	lower := strings.ToLower(strings.TrimSpace(tok))
	if lower == "" {
		return 0, fmt.Errorf("empty key token")
	}
	if k, ok := namedKeys[lower]; ok {
		return k, nil
	}
	if k, ok := modifierKeys[lower]; ok {
		return k, nil
	}
	if len(lower) == 1 {
		b := lower[0]
		if k, ok := letterKeys[b]; ok {
			return k, nil
		}
		if k, ok := digitKeys[b]; ok {
			return k, nil
		}
	}
	return 0, fmt.Errorf("unrecognized key %q", tok)
}

// hotkeyCombo is a parsed browser_hotkey spec: zero or more held modifiers
// plus the one key that's actually pressed and released (e.g. "Control+a"
// → modifiers=[ControlLeft], main=KeyA).
type hotkeyCombo struct {
	modifiers []input.Key
	main      input.Key
}

// parseHotkey splits a "+"-joined spec like "Control+a" or "Enter" into a
// hotkeyCombo. The last token is the key that gets a full press+release;
// every earlier token is held down for its duration.
func parseHotkey(spec string) (hotkeyCombo, error) {
	if strings.TrimSpace(spec) == "" {
		return hotkeyCombo{}, fmt.Errorf("hotkey is required")
	}
	parts := strings.Split(spec, "+")
	var combo hotkeyCombo
	for i, p := range parts {
		k, err := parseKeyToken(p)
		if err != nil {
			return hotkeyCombo{}, fmt.Errorf("browser hotkey %q: %w", spec, err)
		}
		if i == len(parts)-1 {
			combo.main = k
		} else {
			combo.modifiers = append(combo.modifiers, k)
		}
	}
	return combo, nil
}
