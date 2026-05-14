.PHONY: verify-version shellcheck

# Assert that internal/version.Current matches VERSION. Invoked by the
# goreleaser before-hook so a forgotten bump fails the release fast.
# Cutting a release is handled by the `/yottacode:release` slash command,
# not by make.
verify-version:
	@test -n "$(VERSION)" || (echo "VERSION required" && exit 1)
	@grep -q "^const Current = \"$(VERSION)\"$$" internal/version/version.go || \
		(echo "version mismatch: source has $$(grep '^const Current' internal/version/version.go), expected Current = \"$(VERSION)\"" && exit 1)

# Lint install.sh under bash mode. CI runs the same command.
shellcheck:
	@command -v shellcheck >/dev/null 2>&1 || (echo "shellcheck not installed" && exit 1)
	shellcheck -s bash install.sh
