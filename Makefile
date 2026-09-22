.PHONY: verify-version shellcheck homebrew-formula-test

# Assert that internal/version.Current matches VERSION. Invoked by the
# goreleaser before-hook so a forgotten bump fails the release fast.
# Cutting a release is handled by the `/yottacode:release` slash command,
# not by make.
verify-version:
	@test -n "$(VERSION)" || (echo "VERSION required" && exit 1)
	@grep -q "^const Current = \"$(VERSION)\"$$" internal/version/version.go || \
		(echo "version mismatch: source has $$(grep '^const Current' internal/version/version.go), expected Current = \"$(VERSION)\"" && exit 1)

# Lint shell scripts under bash mode. CI runs the same command.
shellcheck:
	@command -v shellcheck >/dev/null 2>&1 || (echo "shellcheck not installed" && exit 1)
	shellcheck -s bash install.sh .github/scripts/generate-homebrew-formula.sh

# Exercise formula generation without network access or an interactive CLI.
homebrew-formula-test:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	printf '%s\n' \
	  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  yottacode_1.2.3_darwin_arm64.tar.gz' \
	  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  yottacode_1.2.3_darwin_amd64.tar.gz' \
	  > "$$tmp/SHA256SUMS"; \
	bash .github/scripts/generate-homebrew-formula.sh 1.2.3 "$$tmp/SHA256SUMS" "$$tmp/Formula/yottacode.rb"; \
	grep -q 'class Yottacode < Formula' "$$tmp/Formula/yottacode.rb"; \
	grep -q 'yottacode_1.2.3_darwin_arm64.tar.gz' "$$tmp/Formula/yottacode.rb"; \
	grep -q 'yottacode_1.2.3_darwin_amd64.tar.gz' "$$tmp/Formula/yottacode.rb"
