package browser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
)

var (
	maxAXNodes = 500
	maxAXChars = 20000
)

func axValueString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var out any
	if json.Unmarshal(v.Value, &out) == nil {
		return fmt.Sprint(out)
	}
	return string(v.Value)
}
func renderAXTree(nodes []*accessibility.Node) string {
	if len(nodes) == 0 {
		return "(empty accessibility tree)"
	}
	by := map[accessibility.NodeID]*accessibility.Node{}
	for _, n := range nodes {
		by[n.NodeID] = n
	}
	root := nodes[0]
	for _, n := range nodes {
		if n.ParentID == "" {
			root = n
			break
		}
	}
	var b strings.Builder
	n := 0
	tr := false
	var walk func(*accessibility.Node, int)
	walk = func(x *accessibility.Node, d int) {
		if x == nil || tr {
			return
		}
		if n >= maxAXNodes || b.Len() >= maxAXChars {
			tr = true
			return
		}
		if !x.Ignored {
			line := strings.Repeat("  ", d) + axValueString(x.Role)
			if s := axValueString(x.Name); s != "" {
				line += fmt.Sprintf(" %q", s)
			}
			if s := axValueString(x.Value); s != "" {
				line += fmt.Sprintf(" =%q", s)
			}
			b.WriteString(line + "\n")
			n++
		}
		for _, id := range x.ChildIDs {
			walk(by[id], d+1)
		}
	}
	walk(root, 0)
	if b.Len() == 0 {
		return "(empty accessibility tree)"
	}
	if tr {
		b.WriteString("…[truncated]")
	}
	return b.String()
}
