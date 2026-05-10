.PHONY: release verify-version

# Cut a release: bump the source version constant, run tests, commit, tag.
# Usage: make release VERSION=0.2.0     (no leading v)
# After this completes, push with:
#   git push origin main && git push origin v$(VERSION)
release:
	@test -n "$(VERSION)" || (echo "VERSION=x.y.z required (no leading v)" && exit 1)
	@git diff --quiet && git diff --cached --quiet || (echo "working tree must be clean before release" && exit 1)
	@grep -q '^const Current = ' internal/version/version.go || (echo "internal/version/version.go: Current constant not found" && exit 1)
	perl -i -pe 's/^const Current = ".*"/const Current = "$(VERSION)"/' internal/version/version.go
	$(MAKE) verify-version VERSION=$(VERSION)
	go test ./...
	@if git diff --quiet -- internal/version/version.go; then \
		echo "version.go already at $(VERSION) in HEAD — skipping bump commit"; \
	else \
		git add internal/version/version.go && \
		git commit -m "release: v$(VERSION)"; \
	fi
	git tag -a v$(VERSION) -m "v$(VERSION)"
	@echo
	@echo "Tagged v$(VERSION). Push with:"
	@echo "  git push origin main && git push origin v$(VERSION)"

# Assert that internal/version.Current matches VERSION. Invoked by the
# goreleaser before-hook so a forgotten bump fails the release fast.
verify-version:
	@test -n "$(VERSION)" || (echo "VERSION required" && exit 1)
	@grep -q "^const Current = \"$(VERSION)\"$$" internal/version/version.go || \
		(echo "version mismatch: source has $$(grep '^const Current' internal/version/version.go), expected Current = \"$(VERSION)\"" && exit 1)
