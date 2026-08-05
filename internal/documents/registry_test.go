package documents

import "testing"

func TestRegistryLookup(t *testing.T) {
	r := NewRegistry()

	supported := []string{"data.csv", "data.tsv", "data.json", "data.jsonl", "data.xml", "data.html", "data.htm"}
	for _, path := range supported {
		if r.Lookup(path) == nil {
			t.Errorf("Lookup(%q) = nil, want an extractor", path)
		}
	}

	if r.Lookup("data.pdf") != nil {
		t.Errorf("Lookup(%q) = non-nil, want nil for an unsupported format", "data.pdf")
	}
}
