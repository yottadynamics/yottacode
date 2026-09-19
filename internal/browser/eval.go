package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// maxEvalChars bounds browser_eval's returned text, same rationale as
// maxAXChars: a page can hand back an arbitrarily large value, and the
// model's context is not free.
const maxEvalChars = 20000

// evalScriptTimeout bounds how long the page's JS may run inside one
// browser_eval (Runtime.evaluate's own terminate-after timeout). Without
// it a `while(true){}` expression would hold the page's main thread — and
// every later call against it — until the whole action deadline.
const evalScriptTimeout = 15 * time.Second

// eval evaluates expression in the active page's main frame with DevTools
// console semantics: it is an expression (not a function body), a returned
// Promise is awaited, and the result comes back by value as JSON.
func (s *session) eval(ctx context.Context, expression string) (string, error) {
	pg := s.activePage().Context(ctx)
	res, err := proto.RuntimeEvaluate{
		Expression:    expression,
		ReturnByValue: true,
		AwaitPromise:  true,
		Timeout:       proto.RuntimeTimeDelta(evalScriptTimeout / time.Millisecond),
	}.Call(pg)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEvalFailed, classifyErr(err, ErrEvalFailed))
	}
	if res.ExceptionDetails != nil {
		return "", fmt.Errorf("%w: %s", ErrEvalFailed, describeException(res.ExceptionDetails))
	}
	return truncateEval(renderEvalResult(res.Result)), nil
}

// describeException renders a thrown JS exception as one readable line
// (the stack, when Chrome supplies one, is dropped — it is noise here).
func describeException(d *proto.RuntimeExceptionDetails) string {
	if d.Exception != nil && d.Exception.Description != "" {
		first, _, _ := strings.Cut(d.Exception.Description, "\n")
		return first
	}
	return d.Text
}

// renderEvalResult renders a Runtime.evaluate result: JSON for a
// serializable value (strings quoted, so "1" and 1 stay distinguishable),
// the special-number spelling for NaN/Infinity/bigint, "undefined", or the
// object's description when it can't be serialized (a DOM node, a function).
func renderEvalResult(r *proto.RuntimeRemoteObject) string {
	if r == nil {
		return "undefined"
	}
	if r.UnserializableValue != "" {
		return string(r.UnserializableValue)
	}
	if r.Type == proto.RuntimeRemoteObjectTypeUndefined {
		return "undefined"
	}
	if !r.Value.Nil() {
		return r.Value.JSON("", "")
	}
	if r.Type == proto.RuntimeRemoteObjectTypeObject && r.Subtype == proto.RuntimeRemoteObjectSubtypeNull {
		return "null"
	}
	if r.Description != "" {
		return r.Description
	}
	return string(r.Type)
}

func truncateEval(s string) string {
	if len(s) <= maxEvalChars {
		return s
	}
	// Cut on a rune boundary so a multi-byte character isn't split.
	cut := maxEvalChars
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("…[truncated, %d of %d bytes shown]", cut, len(s))
}
