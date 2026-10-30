#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="${1:?current dir required}"

BIN_DIR="$CURRENT_DIR/bin"
CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/tmux-ai-attn"
BIN="$BIN_DIR/tmux-ai-attn-sync"
VERSION_FILE="$BIN_DIR/.version"
DEFAULT_VERSION_FILE="$CURRENT_DIR/VERSION"

version="$(cat "$DEFAULT_VERSION_FILE" 2>/dev/null || true)"
if [ -z "$version" ]; then
  printf 'cannot read VERSION file: %s\n' "$DEFAULT_VERSION_FILE" >&2
  exit 1
fi

base_url="https://github.com/cosmicbuffalo/tmux-ai-attn/releases/download"

use_local_build="$(tmux show-option -gqv @ai_attn_dev_build || true)"
use_local_build="${use_local_build:-off}"

platform="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    printf 'unsupported architecture: %s (expected x86_64, aarch64, or arm64)\n' "$arch" >&2
    exit 1
    ;;
esac

case "$platform" in
  linux|darwin) ;;
  *)
    printf 'unsupported platform: %s (expected linux or darwin)\n' "$platform" >&2
    exit 1
    ;;
esac

asset="tmux-ai-attn-sync-${platform}-${arch}"
url="${base_url}/${version}/${asset}"
checksums_url="${base_url}/${version}/checksums.txt"

mkdir -p "$BIN_DIR" "$CACHE_DIR"

# Several servers can prepare binaries at once, from one checkout or from
# several (TPM's and pmux's) sharing CACHE_DIR. So each run downloads and
# builds into files of its own, and renames a file into place only once it
# is complete; renames within a directory are atomic, so nothing ever reads
# a partial checksums file, binary, or version.
TEMP_FILES=()
remove_temp_files() {
  rm -f "${TEMP_FILES[@]+"${TEMP_FILES[@]}"}" 2>/dev/null || true
}
trap remove_temp_files EXIT

# Sets the variable named $1 to a new private file beside $2, to rename over
# it. (Not called in $(...), so the parent shell records the file to clean up.)
temp_beside() {
  local new
  new="$(mktemp "$(dirname "$2")/.$(basename "$2").XXXXXX")"
  TEMP_FILES+=("$new")
  printf -v "$1" '%s' "$new"
}

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1" 2>/dev/null
  else
    return 1
  fi
}

record_version() {
  local file
  temp_beside file "$VERSION_FILE"
  printf '%s' "$version" > "$file"
  mv "$file" "$VERSION_FILE"
}

verify_checksum() {
  local file="$1"
  local checksums_file="$CACHE_DIR/checksums.txt.${version}"
  local expected actual partial

  if [ ! -s "$checksums_file" ]; then
    temp_beside partial "$checksums_file"
    fetch "$checksums_url" "$partial" || return 1
    mv "$partial" "$checksums_file"
  fi

  # checksums.txt format: <hash>  <filename>  (sha256sum output; "*<filename>" in binary mode)
  expected="$(awk -v name="$asset" '$2 == name || $2 == "*"name { print $1; exit }' "$checksums_file")"
  if [ -z "$expected" ]; then
    printf 'checksum entry for %s not found in checksums.txt\n' "$asset" >&2
    return 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{ print $1 }')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{ print $1 }')"
  else
    printf 'no sha256 tool available (sha256sum or shasum)\n' >&2
    return 1
  fi

  if [ "$expected" != "$actual" ]; then
    printf 'checksum mismatch for %s: expected %s, got %s\n' "$asset" "$expected" "$actual" >&2
    return 1
  fi
}

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    printf 'go not found; cannot build tmux-ai-attn-sync from source\n' >&2
    return 1
  fi
  local built
  temp_beside built "$BIN"
  (
    cd "$CURRENT_DIR"
    GOCACHE="${GOCACHE:-$CACHE_DIR/go-build}" go build -o "$built" ./cmd/tmux-ai-attn-sync
  )
  chmod +x "$built"
  mv "$built" "$BIN"
  record_version
}

if [ "$use_local_build" = "on" ]; then
  build_from_source
  exit $?
fi

# Check if the installed binary matches the configured version.
installed_version=""
if [ -f "$VERSION_FILE" ]; then
  installed_version="$(cat "$VERSION_FILE" 2>/dev/null || true)"
fi

if [ -x "$BIN" ] && [ "$installed_version" = "$version" ]; then
  exit 0
fi

# Try downloading a release asset; fall back to building from source.
download=""
temp_beside download "$BIN"
if fetch "$url" "$download"; then
  if verify_checksum "$download"; then
    chmod +x "$download"
    mv "$download" "$BIN"
    record_version
  else
    printf 'downloaded binary failed checksum verification, falling back to source build\n' >&2
    build_from_source
  fi
else
  build_from_source
fi
