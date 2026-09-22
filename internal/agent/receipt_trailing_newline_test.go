package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var endsWithNewlineRe = regexp.MustCompile(`ends_with_newline=(true|false)`)

func endsWithNewline(t *testing.T, out string) string {
	t.Helper()
	m := endsWithNewlineRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("receipt has no ends_with_newline field: %.200q", out)
	}
	return m[1]
}

// "a\nb" and "a\nb\n" render identically; the receipt is the only place the
// model can learn whether the span ends with a newline.
func TestReceipts_ReportWhetherTheSpanEndsWithNewline(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "nonl.txt", "a\nb")
	writeFile(t, tmp, "nl.txt", "a\nb\n")
	writeFile(t, tmp, "crlf.txt", "a\r\nb\r\n")
	cases := []struct {
		name, args, want string
	}{
		{"no trailing newline", `{"path":"nonl.txt","anchors":true}`, "false"},
		{"trailing newline", `{"path":"nl.txt","anchors":true}`, "true"},
		{"crlf trailing newline", `{"path":"crlf.txt","anchors":true}`, "true"},
		{"window cut by limit ends on a line boundary", `{"path":"nonl.txt","anchors":true,"limit":1}`, "true"},
	}
	for _, c := range cases {
		t.Run("read_file/"+c.name, func(t *testing.T) {
			out, err := newReadFile(tmp, false).Execute(context.Background(), c.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := endsWithNewline(t, out); got != c.want {
				t.Errorf("ends_with_newline=%s, want %s in %.160q", got, c.want, out)
			}
		})
	}

	manyCases := []struct {
		name, args, want string
	}{
		{"no trailing newline", `{"paths":["nonl.txt"],"anchors":true}`, "false"},
		{"trailing newline", `{"paths":["nl.txt"],"anchors":true}`, "true"},
		// "a\nb" is 3 bytes: limit 2 cuts it to "a\n", ending on a line boundary.
		{"limit cut on line boundary", `{"paths":["nonl.txt"],"anchors":true,"limit":2}`, "true"},
	}
	for _, c := range manyCases {
		t.Run("read_many_files/"+c.name, func(t *testing.T) {
			out, err := newReadMany(tmp).Execute(context.Background(), c.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := endsWithNewline(t, out); got != c.want {
				t.Errorf("ends_with_newline=%s, want %s in %.160q", got, c.want, out)
			}
		})
	}
}

func TestReadManyFilesTool_EmptyWindowReceiptDoesNotEndWithNewline(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "empty.txt", "")
	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["empty.txt"],"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := endsWithNewline(t, out); got != "false" {
		t.Errorf("ends_with_newline=%s for an empty window, want false", got)
	}
}

// The point of the field: a model that knows nothing but the displayed lines
// and the receipt can build a valid old for apply_hashline.
func TestReceipts_RoundTripThroughApplyHashline(t *testing.T) {
	files := map[string]string{
		"nonl.txt": "alpha\nbeta",
		"nl.txt":   "alpha\nbeta\n",
	}
	readers := map[string]func(dir, name string) (string, error){
		"read_file": func(dir, name string) (string, error) {
			return newReadFile(dir, false).Execute(context.Background(), fmt.Sprintf(`{"path":%q,"anchors":true}`, name))
		},
		"read_many_files": func(dir, name string) (string, error) {
			return newReadMany(dir).Execute(context.Background(), fmt.Sprintf(`{"paths":[%q],"anchors":true}`, name))
		},
	}
	for readerName, read := range readers {
		for name, body := range files {
			t.Run(readerName+"/"+name, func(t *testing.T) {
				tmp := t.TempDir()
				writeFile(t, tmp, name, body)
				out, err := read(tmp, name)
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				m := receiptRe.FindStringSubmatch(out)
				if m == nil {
					t.Fatalf("no receipt: %q", out)
				}
				offset, _ := strconv.Atoi(m[2])
				length, _ := strconv.Atoi(m[3])

				// Rebuild old the way a model would: strip the anchors, join the
				// lines, and add a final newline only if the receipt says so.
				var lines []string
				for _, l := range strings.Split(out, "\n") {
					if anchoredPrefixRe.MatchString(l) {
						lines = append(lines, anchoredPrefixRe.ReplaceAllString(l, ""))
					}
				}
				old := strings.Join(lines, "\n")
				if endsWithNewline(t, out) == "true" {
					old += "\n"
				}
				args, _ := json.Marshal(map[string]any{
					"path": name, "offset": offset, "length": length, "hash": m[4],
					"old": old, "new": strings.ToUpper(old),
				})
				if _, err := newApplyHashline(tmp).Execute(context.Background(), string(args)); err != nil {
					t.Fatalf("apply_hashline rejected an old rebuilt from the receipt: %v\nold=%q length=%d", err, old, length)
				}
				if got, want := readBack(t, tmp, name), strings.ToUpper(body); got != want {
					t.Errorf("file = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestApplyHashlineTool_GuidanceMentionsReceiptFieldsAndInserts(t *testing.T) {
	desc := (&ApplyHashlineTool{}).Description()
	for _, want := range []string{"ends_with_newline", "adjacent", "CRLF"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description should mention %q so the model knows how to build a hunk: %s", want, desc)
		}
	}
	if strings.Contains(desc, "sha256") {
		t.Errorf("description should not invite the model to compute hashes itself: %s", desc)
	}
}
