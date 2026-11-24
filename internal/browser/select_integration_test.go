//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// With several options sharing a value, choosing by label must select THAT
// option, not whichever comes first with the same value.
func TestIntegration_SelectByLabelWithDuplicateValues(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<select id=s onchange="document.getElementById('out').textContent='idx:'+this.selectedIndex">
<option value="">Choose</option><option value="">None</option><option value="">Other</option></select><div id=out></div>`)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	got, err := m.SelectOption(ctx, "#s", "", "Other")
	if err != nil || got != "Other" {
		t.Fatalf("select = %q, %v", got, err)
	}
	if err := m.Wait(ctx, "", "idx:2", false, 5*time.Second); err != nil {
		t.Fatalf("the wrong option was selected: %v", err)
	}
}

// A custom-styled radio hides its <input>; clicking it must fall back to the
// visible label instead of retrying until the deadline.
func TestIntegration_ClickHiddenRadioUsesLabel(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<label for=a>Option A</label><input type=radio id=a name=q style="opacity:0;position:absolute;width:0;height:0;display:none"
onchange="document.getElementById('out').textContent='picked'"><div id=out></div>`)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#a"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := m.Wait(ctx, "", "picked", false, 5*time.Second); err != nil {
		t.Fatalf("radio not selected via label: %v", err)
	}
}

// A selector that also appears in the page's own CSS (a :hover rule) used to
// match the invisible <style> node through chromedp's text search, so the
// element was never "visible" and the click timed out.
func TestIntegration_ClickSelectorAlsoMentionedInStylesheet(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<style>#go:hover{color:red}</style><button id=go onclick="document.title='hit'">Go</button>`)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Click(ctx, "#go"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("click took %s", time.Since(start))
	}
}
