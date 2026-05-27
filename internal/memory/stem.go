package memory

import "strings"

// Stem returns the Porter-stemmed form of a single lowercase English
// token. Implements the Porter 1980 suffix-stripping algorithm (5
// steps). Pure, deterministic, zero allocations beyond the result.
func Stem(word string) string {
	if len(word) <= 2 {
		return word
	}
	w := []byte(strings.ToLower(word))
	w = step1a(w)
	w = step1b(w)
	w = step1c(w)
	w = step2(w)
	w = step3(w)
	w = step4(w)
	w = step5a(w)
	w = step5b(w)
	return string(w)
}

func isConsonant(w []byte, i int) bool {
	switch w[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == 0 {
			return true
		}
		return !isConsonant(w, i-1)
	}
	return true
}

// measure returns the "m" value (number of VC sequences) of w[0:j].
func measure(w []byte) int {
	n := 0
	i := 0
	j := len(w)
	for i < j && isConsonant(w, i) {
		i++
	}
	for i < j {
		for i < j && !isConsonant(w, i) {
			i++
		}
		if i >= j {
			break
		}
		n++
		for i < j && isConsonant(w, i) {
			i++
		}
	}
	return n
}

func hasSuffix(w []byte, s string) bool {
	return len(w) >= len(s) && string(w[len(w)-len(s):]) == s
}

func replaceSuffix(w []byte, old, repl string) []byte {
	stem := w[:len(w)-len(old)]
	return append(stem, repl...)
}

func containsVowel(w []byte) bool {
	for i := range w {
		if !isConsonant(w, i) {
			return true
		}
	}
	return false
}

func endsDoubleConsonant(w []byte) bool {
	l := len(w)
	if l < 2 {
		return false
	}
	return w[l-1] == w[l-2] && isConsonant(w, l-1)
}

// endsCVC checks the *o condition: stem ends consonant-vowel-consonant
// where the final consonant is not w, x, or y.
func endsCVC(w []byte) bool {
	l := len(w)
	if l < 3 {
		return false
	}
	if !isConsonant(w, l-1) || isConsonant(w, l-2) || !isConsonant(w, l-3) {
		return false
	}
	c := w[l-1]
	return c != 'w' && c != 'x' && c != 'y'
}

func step1a(w []byte) []byte {
	if hasSuffix(w, "sses") {
		return replaceSuffix(w, "sses", "ss")
	}
	if hasSuffix(w, "ies") {
		return replaceSuffix(w, "ies", "i")
	}
	if hasSuffix(w, "ss") {
		return w
	}
	if hasSuffix(w, "s") && len(w) > 1 {
		return w[:len(w)-1]
	}
	return w
}

func step1b(w []byte) []byte {
	if hasSuffix(w, "eed") {
		stem := w[:len(w)-3]
		if measure(stem) > 0 {
			return replaceSuffix(w, "eed", "ee")
		}
		return w
	}

	var changed bool
	if hasSuffix(w, "ed") {
		stem := w[:len(w)-2]
		if containsVowel(stem) {
			w = stem
			changed = true
		}
	} else if hasSuffix(w, "ing") {
		stem := w[:len(w)-3]
		if containsVowel(stem) {
			w = stem
			changed = true
		}
	}

	if changed {
		if hasSuffix(w, "at") || hasSuffix(w, "bl") || hasSuffix(w, "iz") {
			w = append(w, 'e')
		} else if endsDoubleConsonant(w) {
			c := w[len(w)-1]
			if c != 'l' && c != 's' && c != 'z' {
				w = w[:len(w)-1]
			}
		} else if measure(w) == 1 && endsCVC(w) {
			w = append(w, 'e')
		}
	}
	return w
}

func step1c(w []byte) []byte {
	if len(w) > 1 && w[len(w)-1] == 'y' {
		stem := w[:len(w)-1]
		if containsVowel(stem) {
			w[len(w)-1] = 'i'
		}
	}
	return w
}

func step2(w []byte) []byte {
	type rule struct{ suffix, repl string }
	rules := []rule{
		{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"},
		{"anci", "ance"}, {"izer", "ize"}, {"abli", "able"},
		{"alli", "al"}, {"entli", "ent"}, {"eli", "e"},
		{"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"},
		{"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"},
		{"fulness", "ful"}, {"ousness", "ous"}, {"aliti", "al"},
		{"iviti", "ive"}, {"biliti", "ble"}, {"logi", "log"},
	}
	for _, r := range rules {
		if hasSuffix(w, r.suffix) {
			stem := w[:len(w)-len(r.suffix)]
			if measure(stem) > 0 {
				return replaceSuffix(w, r.suffix, r.repl)
			}
			return w
		}
	}
	return w
}

func step3(w []byte) []byte {
	type rule struct{ suffix, repl string }
	rules := []rule{
		{"icate", "ic"}, {"ative", ""}, {"alize", "al"},
		{"iciti", "ic"}, {"ical", "ic"}, {"ful", ""},
		{"ness", ""},
	}
	for _, r := range rules {
		if hasSuffix(w, r.suffix) {
			stem := w[:len(w)-len(r.suffix)]
			if measure(stem) > 0 {
				return replaceSuffix(w, r.suffix, r.repl)
			}
			return w
		}
	}
	return w
}

func step4(w []byte) []byte {
	suffixes := []string{
		"al", "ance", "ence", "er", "ic", "able", "ible", "ant",
		"ement", "ment", "ent", "ion", "ou", "ism", "ate", "iti",
		"ous", "ive", "ize",
	}
	for _, s := range suffixes {
		if hasSuffix(w, s) {
			stem := w[:len(w)-len(s)]
			if s == "ion" {
				if len(stem) > 0 && (stem[len(stem)-1] == 's' || stem[len(stem)-1] == 't') {
					if measure(stem) > 1 {
						return stem
					}
				}
			} else {
				if measure(stem) > 1 {
					return stem
				}
			}
			return w
		}
	}
	return w
}

func step5a(w []byte) []byte {
	if len(w) == 0 {
		return w
	}
	if w[len(w)-1] == 'e' {
		stem := w[:len(w)-1]
		if measure(stem) > 1 {
			return stem
		}
		if measure(stem) == 1 && !endsCVC(stem) {
			return stem
		}
	}
	return w
}

func step5b(w []byte) []byte {
	if len(w) == 0 {
		return w
	}
	if measure(w) > 1 && endsDoubleConsonant(w) && w[len(w)-1] == 'l' {
		return w[:len(w)-1]
	}
	return w
}
