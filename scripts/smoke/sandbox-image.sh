#!/bin/sh
# Smoke-test the default sandbox image without network. The fixture is a
# throwaway local module so the test proves the Go toolchain works without
# downloading dependencies from inside the production no-network sandbox.
set -eu

go version
git --version
test -f go.mod

tmp=$(mktemp -d)
cd "$tmp"
go mod init smoke
cat > smoke.go <<'GO'
package smoke

func Add(a, b int) int { return a + b }
GO
cat > smoke_test.go <<'GO'
package smoke

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("bad add")
	}
}
GO
go test -v ./...
