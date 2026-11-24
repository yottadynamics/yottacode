package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
)

func TestDiffDigest(t *testing.T) {
	before := PageDigest{Valid: true, URL: "https://a/q1", Title: "Q1",
		Elements: []string{`radio "A"`, `radio "B"`, `button "Submit"`}}
	after := PageDigest{Valid: true, URL: "https://a/q1", Title: "Q1",
		Elements: []string{`radio "A"`, `radio "B" (checked)`, `button "Submit"`},
		Alerts:   []string{"Saved"}, Dialogs: []string{"alert accepted automatically: hi"}}
	got := DiffDigest(before, after)
	for _, want := range []string{`removed: radio "B"`, `added: radio "B" (checked)`, "alert: Saved", "dialog: alert accepted automatically: hi"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "url:") || strings.Contains(got, "Submit") {
		t.Errorf("unchanged things must not be reported:\n%s", got)
	}
}

func TestDiffDigest_URLTitleAndNoChange(t *testing.T) {
	a := PageDigest{Valid: true, URL: "u1", Title: "t1"}
	if got := DiffDigest(a, a); got != "" {
		t.Errorf("identical digests diff = %q", got)
	}
	b := PageDigest{Valid: true, URL: "u2", Title: "t2"}
	got := DiffDigest(a, b)
	if !strings.Contains(got, "url: u1 -> u2") || !strings.Contains(got, `title: "t1" -> "t2"`) {
		t.Errorf("got %q", got)
	}
	if DiffDigest(PageDigest{}, b) != "" || DiffDigest(a, PageDigest{}) != "" {
		t.Error("an invalid digest must yield no diff")
	}
}

func TestDiffDigest_CapsLongLists(t *testing.T) {
	var els []string
	for i := 0; i < 20; i++ {
		els = append(els, "link \"x"+string(rune('a'+i))+"\"")
	}
	got := DiffDigest(PageDigest{Valid: true}, PageDigest{Valid: true, Elements: els})
	if !strings.Contains(got, "added: …and 12 more") {
		t.Errorf("got:\n%s", got)
	}
}

func TestClickOptionsValidate(t *testing.T) {
	for _, b := range []string{"", "left", "right", "middle"} {
		if err := (ClickOptions{Button: b}).Validate(); err != nil {
			t.Errorf("%q: %v", b, err)
		}
	}
	if err := (ClickOptions{Button: "back"}).Validate(); err == nil {
		t.Error("unknown button accepted")
	}
	if !(ClickOptions{}).plain() || (ClickOptions{Double: true}).plain() || (ClickOptions{Button: "right"}).plain() {
		t.Error("plain() wrong")
	}
}

func TestRefsStableAcrossInspects(t *testing.T) {
	s := &session{}
	mk := func(ids ...int64) []*accessibility.Node {
		nodes := []*accessibility.Node{{NodeID: "0", Role: axVal("RootWebArea")}}
		for _, id := range ids {
			n := &accessibility.Node{NodeID: accessibility.NodeID(string(rune('a' + id))), ParentID: "0", Role: axVal("button"), Name: axVal("b"), BackendDOMNodeID: 0}
			n.BackendDOMNodeID = cdp.BackendNodeID(id)
			nodes[0].ChildIDs = append(nodes[0].ChildIDs, n.NodeID)
			nodes = append(nodes, n)
		}
		return nodes
	}
	first := renderAXTreeRefs(mk(10, 20), s.nextRefs(false))
	if !strings.Contains(first, "[ref=e1]") || !strings.Contains(first, "[ref=e2]") {
		t.Fatalf("first:\n%s", first)
	}
	// Node 20 stays, node 10 goes, node 30 is new: 20 keeps e2, 30 gets e3 (never e1 again).
	second := renderAXTreeRefs(mk(20, 30), s.nextRefs(false))
	if strings.Count(second, "[ref=e2]") != 1 || !strings.Contains(second, "[ref=e3]") || strings.Contains(second, "[ref=e1]") {
		t.Fatalf("second:\n%s", second)
	}
	if _, ok := s.refs.byRef["e1"]; ok {
		t.Error("full inspect must drop refs it no longer sees")
	}
	// A scoped inspect keeps every earlier ref usable.
	_ = renderAXTreeRefs(mk(40), s.nextRefs(true))
	if _, ok := s.refs.byRef["e2"]; !ok {
		t.Error("scoped inspect dropped an earlier ref")
	}
}

func TestPersistentProfileIsNeverDeleted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	m := &Manager{
		findBinary: func() (string, error) { return "/bin/chrome", nil },
		newSession: func(_ context.Context, bin, profile string) (pageSession, error) { return &fakeSession{}, nil },
	}
	if err := m.SetPersistentProfile(dir); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("profile dir not created 0700: %v %v", fi, err)
	}
	marker := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inspect(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := m.Status().ProfileDir; got != dir {
		t.Fatalf("profile dir = %q, want the persistent one", got)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Close deleted the persistent profile: %v", err)
	}
}

func TestThrowawayProfileStillRemoved(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{}
	if err := m.removeProfile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("throwaway profile was not removed")
	}
}

func TestRefMetaPositionAndScoped(t *testing.T) {
	mk := func(id int64, name string) *accessibility.Node {
		return &accessibility.Node{NodeID: accessibility.NodeID(name), Role: axVal("button"), Name: axVal(name), BackendDOMNodeID: cdp.BackendNodeID(id)}
	}
	full := newRefTable()
	r1, r2, r3 := full.assign(mk(1, "Delete")), full.assign(mk(2, "Delete")), full.assign(mk(3, "Save"))
	if m := full.meta[r2]; m.nth != 1 || *m.group != 2 || m.role != "button" || m.name != "Delete" {
		t.Errorf("second Delete meta = %+v (group %d)", m, *full.meta[r2].group)
	}
	if full.meta[r1].nth != 0 || full.meta[r3].nth != 0 || *full.meta[r3].group != 1 {
		t.Errorf("positions wrong: %+v %+v", full.meta[r1], full.meta[r3])
	}
	// A scoped snapshot must not claim a position it cannot know, and must not
	// overwrite the meta of a ref it merely sees again.
	scoped := newRefTable()
	scoped.scoped = true
	scoped.next = 3
	scoped.byNode = map[cdp.BackendNodeID]string{1: r1}
	scoped.meta[r1] = full.meta[r1]
	scoped.assign(mk(1, "Delete"))
	scoped.assign(mk(9, "Delete"))
	if scoped.meta[r1].nth != 0 || scoped.meta[scoped.byNode[9]].nth != -1 {
		t.Errorf("scoped meta = %+v / %+v", scoped.meta[r1], scoped.meta[scoped.byNode[9]])
	}
}

func TestAXForestDFSAndChains(t *testing.T) {
	f := &axForest{nodes: []*accessibility.Node{
		{NodeID: "f1:2", ParentID: "f1:1", Role: axVal("button")},
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []accessibility.NodeID{"2"}},
		{NodeID: "f1:1", ParentID: "2", Role: axVal("RootWebArea"), ChildIDs: []accessibility.NodeID{"f1:2"}},
		{NodeID: "2", ParentID: "1", Role: axVal("Iframe"), ChildIDs: []accessibility.NodeID{"f1:1"}},
	}}
	var got []string
	for _, n := range f.dfs() {
		got = append(got, string(n.NodeID))
	}
	if strings.Join(got, ",") != "1,2,f1:1,f1:2" {
		t.Errorf("dfs order = %v", got)
	}
}
