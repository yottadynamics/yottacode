package usercmd

import "testing"

func TestSubstitute_Arguments(t *testing.T) {
	got := Substitute("review $ARGUMENTS now", "PR 42")
	want := "review PR 42 now"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_PositionalSingle(t *testing.T) {
	got := Substitute("PR #$1 against main", "42")
	want := "PR #42 against main"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_PositionalMulti(t *testing.T) {
	got := Substitute("compare $1 with $2", "foo bar")
	want := "compare foo with bar"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_MissingPositionalEmpty(t *testing.T) {
	// $3 is unsupplied → expands to "".
	got := Substitute("a=$1 b=$2 c=$3", "x y")
	want := "a=x b=y c="
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_DollarTenIsLiteralZero(t *testing.T) {
	// "$10" matches "$1" then leaves "0" as a literal — documented
	// limitation matching Claude Code's surface.
	got := Substitute("price=$10", "5")
	want := "price=50"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_UnreferencedArgsIgnored(t *testing.T) {
	got := Substitute("static body", "extra args supplied")
	want := "static body"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_EmptyArguments(t *testing.T) {
	got := Substitute("hello $ARGUMENTS world", "")
	want := "hello  world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstitute_LiteralDollarOutsidePatternUntouched(t *testing.T) {
	// $A is not a recognized token, leave as-is.
	got := Substitute("env=$ABC", "ignored")
	want := "env=$ABC"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
