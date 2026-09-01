package agent

import (
	"context"
	"strings"
	"testing"
)

func TestReadManyFilesTool_AnchorsIncludeHashlineReceipt(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "alpha\nbeta\n")
	tool := &ReadManyFilesTool{Cwd: NewCwdRef(tmp)}

	out, err := tool.Execute(context.Background(), `{"paths":["a.txt"],"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "# hashline path=a.txt offset=0 length=11 hash=") {
		t.Fatalf("missing hashline receipt: %q", out)
	}
	if !strings.Contains(out, "1#") || !strings.Contains(out, "\talpha") {
		t.Fatalf("missing anchored content lines: %q", out)
	}
}
