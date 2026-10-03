package browser

import (
	"fmt"
	"github.com/chromedp/chromedp/kb"
	"strings"
)

var namedKeys = map[string]string{"enter": kb.Enter, "return": kb.Enter, "escape": kb.Escape, "esc": kb.Escape, "tab": kb.Tab, "backspace": kb.Backspace, "delete": kb.Delete, "del": kb.Delete, "space": " ", "up": kb.ArrowUp, "arrowup": kb.ArrowUp, "down": kb.ArrowDown, "arrowdown": kb.ArrowDown, "left": kb.ArrowLeft, "arrowleft": kb.ArrowLeft, "right": kb.ArrowRight, "arrowright": kb.ArrowRight, "home": kb.Home, "end": kb.End, "pageup": kb.PageUp, "pagedown": kb.PageDown, "f1": kb.F1, "f2": kb.F2, "f3": kb.F3, "f4": kb.F4, "f5": kb.F5, "f6": kb.F6, "f7": kb.F7, "f8": kb.F8, "f9": kb.F9, "f10": kb.F10, "f11": kb.F11, "f12": kb.F12}
var modifierKeys = map[string]string{"control": kb.Control, "ctrl": kb.Control, "shift": kb.Shift, "alt": kb.Alt, "option": kb.Alt, "meta": kb.Meta, "cmd": kb.Meta, "command": kb.Meta, "super": kb.Meta}

func parseKeyToken(tok string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(tok))
	if s == "" {
		return "", fmt.Errorf("empty key token")
	}
	if k, ok := namedKeys[s]; ok {
		return k, nil
	}
	if k, ok := modifierKeys[s]; ok {
		return k, nil
	}
	if len(s) == 1 && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= '0' && s[0] <= '9')) {
		if k, ok := kb.Keys[rune(s[0])]; ok {
			if s[0] >= '0' && s[0] <= '9' {
				return string(s[0]), nil
			}
			return k.Code, nil
		}
	}
	return "", fmt.Errorf("unrecognized key %q", tok)
}

type hotkeyCombo struct {
	modifiers []string
	main      string
}

func parseHotkey(spec string) (hotkeyCombo, error) {
	if strings.TrimSpace(spec) == "" {
		return hotkeyCombo{}, fmt.Errorf("hotkey is required")
	}
	var c hotkeyCombo
	for i, p := range strings.Split(spec, "+") {
		k, e := parseKeyToken(p)
		if e != nil {
			return c, fmt.Errorf("browser hotkey %q: %w", spec, e)
		}
		if i == len(strings.Split(spec, "+"))-1 {
			c.main = k
		} else {
			c.modifiers = append(c.modifiers, k)
		}
	}
	return c, nil
}
