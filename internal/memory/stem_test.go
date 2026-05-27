package memory

import "testing"

func TestStem_BasicSuffixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"running", "run"},
		{"tests", "test"},
		{"testing", "test"},
		{"tested", "test"},
		{"caresses", "caress"},
		{"ponies", "poni"},
		{"ties", "ti"},
		{"cats", "cat"},
		{"feed", "feed"},
		{"agreed", "agre"},
		{"plastered", "plaster"},
		{"bled", "bled"},
		{"motoring", "motor"},
		{"sing", "sing"},
		{"conflated", "conflat"},
		{"troubled", "troubl"},
		{"sized", "size"},
		{"hopping", "hop"},
		{"tanned", "tan"},
		{"falling", "fall"},
		{"hissing", "hiss"},
		{"fizzing", "fizz"},
		{"failing", "fail"},
		{"filing", "file"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_Step1c(t *testing.T) {
	cases := []struct{ in, want string }{
		{"happy", "happi"},
		{"sky", "sky"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_Step2(t *testing.T) {
	cases := []struct{ in, want string }{
		{"relational", "relat"},
		{"conditional", "condit"},
		{"rational", "ration"},
		{"valenci", "valenc"},
		{"hesitanci", "hesit"},
		{"digitizer", "digit"},
		{"conformabli", "conform"},
		{"radicalli", "radic"},
		{"differentli", "differ"},
		{"vileli", "vile"},
		{"analogousli", "analog"},
		{"vietnamization", "vietnam"},
		{"predication", "predic"},
		{"operator", "oper"},
		{"feudalism", "feudal"},
		{"decisiveness", "decis"},
		{"hopefulness", "hope"},
		{"callousness", "callous"},
		{"formaliti", "formal"},
		{"sensitiviti", "sensit"},
		{"sensibiliti", "sensibl"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_Step3(t *testing.T) {
	cases := []struct{ in, want string }{
		{"triplicate", "triplic"},
		{"formative", "form"},
		{"formalize", "formal"},
		{"electriciti", "electr"},
		{"electrical", "electr"},
		{"hopeful", "hope"},
		{"goodness", "good"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_Step4(t *testing.T) {
	cases := []struct{ in, want string }{
		{"revival", "reviv"},
		{"allowance", "allow"},
		{"inference", "infer"},
		{"airliner", "airlin"},
		{"gyroscopic", "gyroscop"},
		{"adjustable", "adjust"},
		{"defensible", "defens"},
		{"irritant", "irrit"},
		{"replacement", "replac"},
		{"adjustment", "adjust"},
		{"dependent", "depend"},
		{"adoption", "adopt"},
		{"homologou", "homolog"},
		{"communism", "commun"},
		{"activate", "activ"},
		{"angulariti", "angular"},
		{"homologous", "homolog"},
		{"effective", "effect"},
		{"bowdlerize", "bowdler"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_Step5(t *testing.T) {
	cases := []struct{ in, want string }{
		{"probate", "probat"},
		{"rate", "rate"},
		{"cease", "ceas"},
		{"controll", "control"},
		{"roll", "roll"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem_ShortWords(t *testing.T) {
	if got := Stem("go"); got != "go" {
		t.Errorf("Stem(%q) = %q, want %q", "go", got, "go")
	}
	if got := Stem("a"); got != "a" {
		t.Errorf("Stem(%q) = %q, want %q", "a", got, "a")
	}
	if got := Stem(""); got != "" {
		t.Errorf("Stem(%q) = %q, want %q", "", got, "")
	}
}

func TestStem_CaseFolding(t *testing.T) {
	if got := Stem("Running"); got != "run" {
		t.Errorf("Stem(%q) = %q, want %q", "Running", got, "run")
	}
}

func TestStem_DevTerms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"migrations", "migrat"},
		{"configured", "configur"},
		{"preferences", "prefer"},
		{"integration", "integr"},
		{"deployment", "deploy"},
		{"authentication", "authent"},
		{"permissions", "permiss"},
		{"dependencies", "depend"},
		{"refactoring", "refactor"},
		{"debugging", "debug"},
	}
	for _, tc := range cases {
		if got := Stem(tc.in); got != tc.want {
			t.Errorf("Stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
