package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ResponseBody is the body of one response the page received.
type ResponseBody struct {
	RequestID string
	URL       string
	MIMEType  string
	Status    int
	// Body is the window of the response text asked for. Empty for binary
	// responses (Binary is set instead).
	Body   string
	Binary bool
	// Size is the whole response's length in bytes.
	Size int
	// Offset is where Body starts; NextOffset is where to continue, or 0 when
	// the response has been read to its end.
	Offset     int
	NextOffset int
	Truncated  bool
}

// DefaultResponseBodyBytes and MaxResponseBodyBytes bound what one
// browser_response_body call hands back to the model. The maximum equals the
// tool layer's cap on page-derived text, so a window is never cut a second
// time on its way out; larger bodies are read in pieces with an offset.
const (
	DefaultResponseBodyBytes = 32 << 10
	MaxResponseBodyBytes     = 40000
)

// windowBody returns up to max bytes of body starting at offset, on rune
// boundaries at both ends, and the offset to continue from (0 when the body has
// been read to its end).
func windowBody(body []byte, offset, max int) (window []byte, next int) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(body) {
		return nil, 0
	}
	// Don't start in the middle of a character.
	for i := 0; i < utf8.UTFMax-1 && offset < len(body) && !utf8.RuneStart(body[offset]) && offset > 0; i++ {
		offset++
	}
	window = cutOnRune(body[offset:], max)
	if end := offset + len(window); end < len(body) {
		return window, end
	}
	return window, 0
}

// MaxUploadBytes is the fixed safety limit for one browser_upload call's total
// payload, mirroring MaxDownloadBytes.
const MaxUploadBytes int64 = 100 << 20

// ValidateUploadFiles checks that every path is a regular file and that the
// total size is within MaxUploadBytes. Callers still own path-trust checks.
func ValidateUploadFiles(paths []string) error {
	var total int64
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("browser upload: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("browser upload: %s is not a regular file", p)
		}
		total += fi.Size()
	}
	if total > MaxUploadBytes {
		return fmt.Errorf("%w: %d bytes (limit %d)", ErrUploadTooLarge, total, MaxUploadBytes)
	}
	return nil
}

// responseBody fetches the body of a request the active page made (an id from
// browser_network_requests). Binary payloads are described, not returned.
func (s *session) responseBody(ctx context.Context, requestID string, maxBytes, offset int) (ResponseBody, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultResponseBodyBytes
	}
	if maxBytes > MaxResponseBodyBytes {
		maxBytes = MaxResponseBodyBytes
	}
	p := s.activePage()
	out := ResponseBody{RequestID: requestID}
	p.mu.Lock()
	for i := len(p.network) - 1; i >= 0; i-- { // last hop: redirects reuse the id
		if e := p.network[i]; e.RequestID == requestID {
			out.URL, out.MIMEType, out.Status = e.URL, e.MIMEType, e.Status
			break
		}
	}
	p.mu.Unlock()
	if out.URL == "" {
		return out, fmt.Errorf("browser response body: no buffered request with id %q (see browser_network_requests; only the most recent %d are kept)", requestID, maxBufferedEntries)
	}
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var body []byte
	err := chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		body, err = network.GetResponseBody(network.RequestID(requestID)).Do(ctx)
		return err
	}))
	if err != nil {
		return out, fmt.Errorf("browser response body: %w (the response may have been evicted, redirected, or never completed)", err)
	}
	out.Size = len(body)
	if bytes.IndexByte(body[:min(len(body), 4096)], 0) >= 0 || strings.HasPrefix(out.MIMEType, "image/") || strings.HasPrefix(out.MIMEType, "audio/") || strings.HasPrefix(out.MIMEType, "video/") {
		out.Binary = true
		return out, nil
	}
	window, next := windowBody(body, offset, maxBytes)
	actualOffset := max(offset, 0)
	for actualOffset < len(body) && !utf8.RuneStart(body[actualOffset]) {
		actualOffset++
	}
	out.Offset, out.NextOffset, out.Truncated = actualOffset, next, next != 0 || actualOffset > 0
	out.Body = string(window)
	return out, nil
}

// cutOnRune returns body cut to at most max bytes without splitting a UTF-8
// character: it backs up at most three bytes, whatever the rest of the body
// contains (a Latin-1 page is not valid UTF-8 and must not be trimmed to nothing).
func cutOnRune(body []byte, max int) []byte {
	if len(body) <= max {
		return body
	}
	cut := max
	// Back up over at most the continuation bytes of one character. If the
	// bytes there are not UTF-8 at all, stop: the body is not text we can
	// repair, and walking further would trim it away.
	for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(body[cut]); i++ {
		cut--
	}
	return body[:cut]
}

// selectOptionJS picks an <option> by value, falling back to its visible label,
// and fires the input/change events frameworks listen for. Returns "ok:<label>"
// or "err:<reason>".
const selectOptionJS = `function(value, label) {
	if (!this || this.tagName !== 'SELECT') return 'err:element is not a <select>';
	value = value == null ? '' : String(value);
	label = label == null ? '' : String(label);
	const opts = Array.from(this.options);
	const norm = s => (s || '').trim().toLowerCase();
	let o = value !== '' ? opts.find(x => x.value === value) : undefined;
	if (!o && label !== '') o = opts.find(x => (x.label || x.text).trim() === label.trim()) || opts.find(x => norm(x.label || x.text) === norm(label));
	if (!o && value !== '' && label === '') o = opts.find(x => norm(x.label || x.text) === norm(value));
	if (!o) return 'err:no option matches; available: ' + opts.slice(0, 30).map(x => JSON.stringify(x.value) + ' (' + JSON.stringify((x.label || x.text).trim().slice(0, 60)) + ')').join(', ');
	if (o.disabled) return 'err:option is disabled';
	this.selectedIndex = o.index;
	this.dispatchEvent(new Event('input', {bubbles: true}));
	this.dispatchEvent(new Event('change', {bubbles: true}));
	return 'ok:' + (o.label || o.text).trim();
}`

func (s *session) selectOption(ctx context.Context, sel, value, label string) (string, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	inner, opts, err := s.query(c, sel)
	if err != nil {
		return "", err
	}
	var nodes []*cdp.Node
	lc, lcancel := context.WithTimeout(c, selectorLookupTimeout)
	err = chromedp.Run(lc, chromedp.Nodes(inner, &nodes, append(opts, chromedp.AtLeast(1))...))
	lcancel()
	if err != nil {
		return "", classifySelectorErr(err)
	}
	v, _ := json.Marshal(value)
	l, _ := json.Marshal(label)
	var result string
	err = chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		obj, err := dom.ResolveNode().WithNodeID(nodes[0].NodeID).Do(ctx)
		if err != nil {
			return err
		}
		res, ex, err := runtime.CallFunctionOn(selectOptionJS).WithObjectID(obj.ObjectID).
			WithArguments([]*runtime.CallArgument{{Value: v}, {Value: l}}).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if ex != nil {
			if ex.Exception != nil && ex.Exception.Description != "" {
				return fmt.Errorf("select failed: %s", ex.Exception.Description)
			}
			return fmt.Errorf("select failed: %s", ex.Text)
		}
		return json.Unmarshal(res.Value, &result)
	}))
	if err != nil {
		return "", classifySelectorErr(err)
	}
	if msg, ok := strings.CutPrefix(result, "err:"); ok {
		return "", fmt.Errorf("browser select: %s", msg)
	}
	return strings.TrimPrefix(result, "ok:"), nil
}
