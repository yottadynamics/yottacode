package agent

import "testing"

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&ReadFileTool{Cwd: "/x"})
	r.Register(&WriteFileTool{Cwd: "/x"})

	if got, ok := r.Get("read_file"); !ok || got.Name() != "read_file" {
		t.Errorf("Get(read_file): ok=%v name=%q", ok, got)
	}
	if _, ok := r.Get("missing"); ok {
		t.Errorf("Get(missing) should be !ok")
	}
}

func TestRegistry_AsAdapterTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&ReadFileTool{Cwd: "/x"})
	r.Register(&WriteFileTool{Cwd: "/x"})
	r.Register(&ReadManyFilesTool{Cwd: "/x"})

	tools := r.AsAdapterTools()
	if len(tools) != 3 {
		t.Fatalf("AsAdapterTools len = %d, want 3", len(tools))
	}
	seen := map[string]bool{}
	for _, to := range tools {
		seen[to.Name] = true
		if to.Schema == nil {
			t.Errorf("%s: missing schema", to.Name)
		}
		if to.Description == "" {
			t.Errorf("%s: missing description", to.Name)
		}
	}
	if !seen["read_file"] || !seen["write_file"] || !seen["read_many_files"] {
		t.Errorf("AsAdapterTools didn't surface all tools: %+v", seen)
	}
}
