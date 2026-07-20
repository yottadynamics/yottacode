package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveMaintenance_ListAndPruneArchives(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	memPath, err := MemoryFilePath("user", "prefs", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(memPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(RenderFrontmatter("prefs", "user", "Prefs", time.Now())+"v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldArchive, err := ArchivePrior(memPath, "100")
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -120)
	if err := os.Chtimes(oldArchive, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(RenderFrontmatter("prefs", "user", "Prefs", time.Now())+"v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newArchive, err := ArchivePrior(memPath, "200")
	if err != nil {
		t.Fatal(err)
	}

	summaries, err := ListArchiveSummaries("user", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Memory != "prefs" || summaries[0].Count != 2 {
		t.Fatalf("summaries = %+v, want one prefs summary with 2 archives", summaries)
	}

	dryRun, err := PruneArchives(cwd, ArchivePruneOptions{Scope: "user", OlderThanDays: 90, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Matched != 1 || dryRun.Deleted != 0 {
		t.Fatalf("dryRun = %+v, want one matched and zero deleted", dryRun)
	}
	if _, err := os.Stat(oldArchive); err != nil {
		t.Fatalf("dry run should keep old archive: %v", err)
	}

	pruned, err := PruneArchives(cwd, ArchivePruneOptions{Scope: "user", KeepLatest: 1, DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Matched != 1 || pruned.Deleted != 1 {
		t.Fatalf("pruned = %+v, want one deleted", pruned)
	}
	if _, err := os.Stat(oldArchive); !os.IsNotExist(err) {
		t.Fatalf("old archive should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(newArchive); err != nil {
		t.Fatalf("new archive should be kept: %v", err)
	}
}

func TestRecordCurationHistory_AppendsJSONL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOTTACODE_HOME", "")
	cwd := t.TempDir()
	if err := RecordCurationHistory("user", "prefs", cwd, CurationHistoryRecord{Action: "move-portable", From: "project/prefs", To: "user/prefs", Reason: "audit issue portable-in-project"}); err != nil {
		t.Fatal(err)
	}
	dir, err := UserMemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, HistoryDirName, "prefs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec CurationHistoryRecord
	if err := json.Unmarshal(data[:len(data)-1], &rec); err != nil {
		t.Fatalf("history is not JSONL: %v\n%s", err, data)
	}
	if rec.Action != "move-portable" || rec.From != "project/prefs" || rec.To != "user/prefs" || rec.Reason == "" {
		t.Fatalf("history record = %+v", rec)
	}
}
