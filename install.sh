#!/usr/bin/env bash
#
# yottacode installer — fresh install and in-place upgrade.
# https://github.com/yottadynamics/yottacode
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh | bash
#
# Env:
#   VERSION       Pin a version (e.g. 0.4.0). Defaults to latest release.
#   INSTALL_DIR   Override install location. Defaults to $HOME/.yottacode/bin.
#   NO_COLOR      Disable ANSI colors.
#
# Flags:
#   --no-modify-rc   Do not edit any shell rc file.
#   --help, -h       Show this help.

set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────────────────
REPO="yottadynamics/yottacode"
BIN_NAME="yottacode"
DEFAULT_INSTALL_DIR="${HOME}/.yottacode/bin"
# shellcheck disable=SC2016  # literal $HOME shown to the user in messages
PATH_BIN_DIR='$HOME/.yottacode/bin'
TOTAL_STEPS=5

NO_MODIFY_RC=0
for arg in "$@"; do
  case "$arg" in
    --no-modify-rc) NO_MODIFY_RC=1 ;;
    -h|--help)
      sed -n '2,18p' "${BASH_SOURCE[0]:-$0}" 2>/dev/null | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      printf 'unknown option: %s\n' "$arg" >&2
      exit 2
      ;;
  esac
done

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
VERSION="${VERSION:-latest}"

# ─────────────────────────────────────────────────────────────────────────────
# Color + glyphs
# ─────────────────────────────────────────────────────────────────────────────
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  TTY_OUT=1
  B=$'\033[1m'; D=$'\033[2m'
  R=$'\033[31m'; G=$'\033[32m'; Y=$'\033[33m'; C=$'\033[36m'
  N=$'\033[0m'
else
  TTY_OUT=0
  B=''; D=''; R=''; G=''; Y=''; C=''; N=''
fi

if printf '%s' "${LANG:-}${LC_ALL:-}" | grep -qi 'utf'; then
  G_OK="✓"; G_NO="✗"; G_WARN="!"; G_DOT="•"
  BAR_FILL="█"; BAR_EMPTY="░"
else
  G_OK="OK"; G_NO="X"; G_WARN="!"; G_DOT="*"
  BAR_FILL="#"; BAR_EMPTY="-"
fi

COLS=$(command -v tput >/dev/null 2>&1 && tput cols 2>/dev/null || echo 80)
[ "$COLS" -ge 40 ] || COLS=80

# ─────────────────────────────────────────────────────────────────────────────
# Trailing footer — a 3-row bar (blank line, HR, bar) that follows the last
# printed line. erase_footer rewinds 3 rows so the next ✓ line can replace
# the bar in place; the next step then redraws the footer one row lower.
# No scroll region, no reserved area, no gaps. Non-TTY skips the footer
# entirely so logs and pipes stay tight.
# ─────────────────────────────────────────────────────────────────────────────
FOOTER_HEIGHT=3
FOOTER_DRAWN=0

repeat_char() {
  local ch=$1 n=$2 out=""
  [ "$n" -le 0 ] && return 0
  local i
  for ((i=0; i<n; i++)); do out+="$ch"; done
  printf '%s' "$out"
}

draw_footer() {
  [ "$TTY_OUT" = 1 ] || return 0
  local pct=$1 step_n=$2 label=$3
  local suffix
  printf -v suffix '%3d%% (%d/%d)' "$pct" "$step_n" "$TOTAL_STEPS"
  local barw=$((COLS - ${#suffix} - ${#label} - 6))
  [ "$barw" -gt 48 ] && barw=48
  [ "$barw" -lt 12 ] && barw=12
  local filled=$((barw * pct / 100))
  local empty=$((barw - filled))
  local fill_str empty_str
  fill_str=$(repeat_char "$BAR_FILL"  "$filled")
  empty_str=$(repeat_char "$BAR_EMPTY" "$empty")

  printf '\n'
  printf '%s%s%s%s%s%s %s%s%s  %s%s%s\n' \
    "$C" "$fill_str" "$N" "$D" "$empty_str" "$N" \
    "$B" "$suffix" "$N" "$D" "$label" "$N"
  printf '\n'
  FOOTER_DRAWN=1
}

erase_footer() {
  [ "$TTY_OUT" = 1 ] || return 0
  [ "$FOOTER_DRAWN" = 1 ] || return 0
  local i
  for ((i=0; i<FOOTER_HEIGHT; i++)); do
    printf '\033[1A\033[K'
  done
  FOOTER_DRAWN=0
}

# ─────────────────────────────────────────────────────────────────────────────
# Output helpers
# ─────────────────────────────────────────────────────────────────────────────
LABEL_W=11

ok()     { printf '  %s%s%s %-*s %s\n'    "$G" "$G_OK"   "$N" "$LABEL_W" "$1" "$2"; }
detail() { printf '  %s%s %s%s\n'         "$D" "$G_DOT"  "$1" "$N"; }
warn()   { printf '  %s%s%s %s%s%s\n'     "$Y" "$G_WARN" "$N" "$Y" "$1" "$N"; }
fail()   { erase_footer; printf '\n  %s%s %s%s\n\n' "$R$B" "$G_NO" "$1" "$N" >&2; exit 1; }

# ─────────────────────────────────────────────────────────────────────────────
# Tool wrappers
# ─────────────────────────────────────────────────────────────────────────────
fetch() {
  local url=$1 out=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url"
  else
    fail "neither curl nor wget is installed"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "no sha256 utility found (need sha256sum or shasum)"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Concurrent-install guard + cleanup
# ─────────────────────────────────────────────────────────────────────────────
LOCK_DIR="${HOME}/.yottacode/cache/install.lock"
mkdir -p "$(dirname "$LOCK_DIR")" >/dev/null 2>&1 || true
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  fail "another yottacode install is in progress (lock: $LOCK_DIR — remove if stale)"
fi

TMPDIR_INSTALL=$(mktemp -d 2>/dev/null || mktemp -d -t yottacode)

cleanup() {
  local ec=$?
  rmdir "$LOCK_DIR" 2>/dev/null || true
  rm -rf "$TMPDIR_INSTALL" 2>/dev/null || true
  return "$ec"
}
trap cleanup EXIT INT TERM

# ─────────────────────────────────────────────────────────────────────────────
# Steps — each draws its starting footer, does work, erases footer, prints
# the ✓ completion line. The next step's draw_footer re-emits the bar one
# row lower, so the bar always trails the latest content.
# ─────────────────────────────────────────────────────────────────────────────
detect_platform() {
  draw_footer 0 1 "Detecting platform"
  local os arch
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$(uname -m)" in
    x86_64|amd64)  arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
  case "$os" in
    linux|darwin) ;;
    *) fail "unsupported OS: $os (Windows users: install under WSL)" ;;
  esac
  OS=$os
  ARCH=$arch
  erase_footer
  ok "platform" "${B}${os}/${arch}${N}"
}

resolve_version() {
  draw_footer 20 2 "Resolving release version"
  if [ "$VERSION" = "latest" ] || [ -z "$VERSION" ]; then
    local tag_file="$TMPDIR_INSTALL/tag.json"
    fetch "https://api.github.com/repos/$REPO/releases/latest" "$tag_file" \
      || fail "could not reach api.github.com (set VERSION=<pin> to skip)"
    VERSION=$(grep -E '"tag_name"' "$tag_file" | head -n1 \
              | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/')
    [ -n "$VERSION" ] || fail "could not parse latest release tag from GitHub response"
  fi
  VERSION=${VERSION#v}
  erase_footer
  ok "version" "${B}${VERSION}${N}"
}

download() {
  draw_footer 40 3 "Downloading ${BIN_NAME} ${VERSION}"
  ARCHIVE="${BIN_NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"
  ARCHIVE_PATH="$TMPDIR_INSTALL/$ARCHIVE"
  SUMS_PATH="$TMPDIR_INSTALL/SHA256SUMS"
  local base="https://github.com/$REPO/releases/download/v${VERSION}"
  fetch "$base/$ARCHIVE" "$ARCHIVE_PATH" || fail "download failed: $base/$ARCHIVE"
  fetch "$base/SHA256SUMS" "$SUMS_PATH" \
    || fail "could not download SHA256SUMS — refusing to install unverified binary"
  local size
  size=$(du -h "$ARCHIVE_PATH" 2>/dev/null | awk '{print $1}')
  erase_footer
  ok "archive" "${B}${size}${N}  ${D}(${ARCHIVE})${N}"
}

verify() {
  draw_footer 60 4 "Verifying checksum"
  local expected actual
  expected=$(grep -E " ${ARCHIVE}\$" "$SUMS_PATH" | awk '{print $1}')
  [ -n "$expected" ] || fail "no SHA256SUMS entry for $ARCHIVE"
  actual=$(sha256_of "$ARCHIVE_PATH")
  if [ "$expected" != "$actual" ]; then
    fail "checksum mismatch for $ARCHIVE (expected $expected, got $actual)"
  fi
  erase_footer
  ok "sha256" "${D}${actual:0:8}…${N}"
}

install_bin() {
  draw_footer 80 5 "Installing ${BIN_NAME} ${VERSION}"
  mkdir -p "${INSTALL_DIR}"
  chmod 0700 "$INSTALL_DIR" 2>/dev/null || true
  (cd "$TMPDIR_INSTALL" && tar -xzf "$ARCHIVE") || fail "tar extract failed"
  local extracted="$TMPDIR_INSTALL/${BIN_NAME}"
  [ -f "$extracted" ] || fail "archive missing '${BIN_NAME}' binary"
  install -m 0755 "$extracted" "$INSTALL_DIR/$BIN_NAME" \
    || fail "install -m 0755 failed (check $INSTALL_DIR is writable)"
  erase_footer
  ok "binary" "${B}${INSTALL_DIR}/${BIN_NAME}${N}"

  if command -v "${BIN_NAME}" >/dev/null 2>&1; then
    local existing
    existing="$(command -v "${BIN_NAME}")"
    if [ "${existing}" != "${INSTALL_DIR}/${BIN_NAME}" ]; then
      warn   "an older ${BIN_NAME} shadows this install"
      detail "found at: ${existing}"
      detail "remove with: ${B}sudo rm ${existing}${N}"
    fi
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# PATH wiring — auto-append (no prompt) with idempotent sentinel + backup.
# ─────────────────────────────────────────────────────────────────────────────
PATH_STATUS=""
RC_FILE=""
SHELL_NAME=""

wire_path() {
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) PATH_STATUS="on_path"; return ;;
  esac
  if [ "$NO_MODIFY_RC" = 1 ]; then
    PATH_STATUS="skipped"
    return
  fi

  SHELL_NAME=$(basename "${SHELL:-/bin/sh}")
  case "$SHELL_NAME" in
    zsh)  RC_FILE="${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash) if [ "${OS:-linux}" = darwin ]; then RC_FILE="$HOME/.bash_profile"
          else RC_FILE="$HOME/.bashrc"; fi ;;
    fish) RC_FILE="$HOME/.config/fish/config.fish" ;;
    *)    RC_FILE="$HOME/.profile" ;;
  esac

  if [ -f "$RC_FILE" ] && grep -Fq "# >>> yottacode >>>" "$RC_FILE"; then
    PATH_STATUS="already_configured"
    return
  fi

  mkdir -p "$(dirname "$RC_FILE")"
  [ -f "$RC_FILE" ] && cp "$RC_FILE" "$RC_FILE.yottacode-backup-$(date +%Y%m%d%H%M%S)"
  {
    printf '\n# >>> yottacode >>>\n'
    if [ "$SHELL_NAME" = fish ]; then
      # shellcheck disable=SC2016  # literal $HOME goes into the rc file
      printf 'fish_add_path -p "$HOME/.yottacode/bin"\n'
    else
      # shellcheck disable=SC2016  # literal $HOME / $PATH go into the rc file
      printf 'export PATH="$HOME/.yottacode/bin:$PATH"\n'
    fi
    printf '# <<< yottacode <<<\n'
  } >> "$RC_FILE"
  PATH_STATUS="added"
}

summary() {
  printf '\n  %s%s installed%s  %s%s%s\n' "$G$B" "$G_OK" "$N" "$B" "$VERSION" "$N"
  printf '  %sLocation%s  %s\n' "$D" "$N" "${INSTALL_DIR}/${BIN_NAME}"
  case "$PATH_STATUS" in
    on_path)
      printf '  %sPATH%s      already on PATH\n' "$D" "$N" ;;
    already_configured)
      printf '  %sPATH%s      rc already configured (%s)\n' "$D" "$N" "$RC_FILE" ;;
    added)
      printf '  %sPATH%s      added to %s%s%s (backup saved)\n' \
        "$D" "$N" "$C" "$RC_FILE" "$N" ;;
    skipped|*)
      printf '  %sPATH%s      add %s%s%s to your shell rc\n' \
        "$D" "$N" "$C" "$PATH_BIN_DIR" "$N" ;;
  esac
  case "$PATH_STATUS" in
    added|already_configured)
      printf '  %sRun%s %ssource %s%s, then %s%s%s.\n' \
        "$D" "$N" "$B" "$RC_FILE" "$N" "$B" "$BIN_NAME" "$N" ;;
    on_path)
      printf '  %sRun%s %s%s%s to get started.\n' "$D" "$N" "$B" "$BIN_NAME" "$N" ;;
    *)
      printf '  %sThen run%s %s%s%s.\n' "$D" "$N" "$B" "$BIN_NAME" "$N" ;;
  esac
}

# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────
detect_platform
resolve_version
download
verify
install_bin
wire_path
summary
draw_footer 100 5 "Done"   # final bar — bottom-most line, all text sits above it
