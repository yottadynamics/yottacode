package browser

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestRenderEvalResult(t *testing.T) {
	cases := []struct {
		name string
		obj  *proto.RuntimeRemoteObject
		want string
	}{
		{"nil", nil, "undefined"},
		{"undefined", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeUndefined}, "undefined"},
		{"number", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeNumber, Value: gson.New(42)}, "42"},
		{"string is quoted", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeString, Value: gson.New("1")}, `"1"`},
		{"object", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeObject, Value: gson.New(map[string]any{"a": 1})}, `{"a":1}`},
		{"NaN", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeNumber, UnserializableValue: "NaN"}, "NaN"},
		{"bigint", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeBigint, UnserializableValue: "10n"}, "10n"},
		{"null", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeObject, Subtype: proto.RuntimeRemoteObjectSubtypeNull}, "null"},
		{"DOM node falls back to its description", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeObject, Description: "body"}, "body"},
		{"last resort is the type", &proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeFunction}, "function"},
	}
	for _, c := range cases {
		if got := renderEvalResult(c.obj); got != c.want {
			t.Errorf("%s: renderEvalResult = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDescribeException(t *testing.T) {
	withStack := &proto.RuntimeExceptionDetails{
		Text:      "Uncaught",
		Exception: &proto.RuntimeRemoteObject{Description: "ReferenceError: x is not defined\n    at <anonymous>:1:1"},
	}
	if got := describeException(withStack); got != "ReferenceError: x is not defined" {
		t.Errorf("describeException = %q, want the first line only", got)
	}
	if got := describeException(&proto.RuntimeExceptionDetails{Text: "Uncaught"}); got != "Uncaught" {
		t.Errorf("describeException without an exception object = %q", got)
	}
}

func TestTruncateEval(t *testing.T) {
	if got := truncateEval("short"); got != "short" {
		t.Errorf("short input changed: %q", got)
	}
	long := strings.Repeat("a", maxEvalChars+500)
	got := truncateEval(long)
	if !strings.Contains(got, "…[truncated, 20000 of 20500 bytes shown]") {
		t.Errorf("missing truncation note: ...%s", got[len(got)-60:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxEvalChars)) {
		t.Error("truncation should keep the first maxEvalChars bytes")
	}

	// A multi-byte rune straddling the limit must not be split.
	multi := strings.Repeat("a", maxEvalChars-1) + "€€€"
	got = truncateEval(multi)
	if !utf8.ValidString(got) {
		t.Errorf("truncation split a multi-byte rune: %q", got[len(got)-30:])
	}
}
