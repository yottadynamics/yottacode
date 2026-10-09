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
