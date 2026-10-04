package subagents

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"pass", "### Check: x\nok\n\nVERDICT: PASS", VerdictPass},
		{"fail with trailing newline", "bad\nVERDICT: FAIL\n", VerdictFail},
		{"partial", "VERDICT: PARTIAL", VerdictPartial},
		{"trailing spaces", "VERDICT: PASS   \n\n", VerdictPass},
		{"bold is not accepted", "**VERDICT: PASS**", ""},
		{"punctuation is not accepted", "VERDICT: PASS.", ""},
		{"lowercase is not accepted", "VERDICT: pass", ""},
		{"verdict not on the last line", "VERDICT: PASS\nbut then I kept talking", ""},
		{"unknown verdict", "VERDICT: MAYBE", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := ParseVerdict(c.in); got != c.want {
			t.Errorf("%s: ParseVerdict(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
