#!/usr/bin/env bash
# Two plugin checkouts (say TPM's and pmux's) share one download cache. Their
# servers can prepare the helper binary at the same time; each must end up
# with an intact, verified binary.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
fake_bin="$tmp_dir/fake-bin"
cache="$tmp_dir/cache"

cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

mkdir -p "$fake_bin" "$cache"

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  *) arch=arm64 ;;
esac
asset="tmux-ai-attn-sync-$(uname -s | tr '[:upper:]' '[:lower:]')-$arch"

# The release asset: large enough that a slow download is visibly partial.
asset_file="$tmp_dir/asset"
{ printf '#!/bin/sh\necho fake daemon\n#'; head -c 200000 /dev/zero | tr '\0' 'x'; printf '\n'; } > "$asset_file"
if command -v sha256sum >/dev/null 2>&1; then
  sum="$(sha256sum "$asset_file" | awk '{ print $1 }')"
else
  sum="$(shasum -a 256 "$asset_file" | awk '{ print $1 }')"
fi
printf '%s  %s\n' "$sum" "$asset" > "$tmp_dir/checksums.txt"

# A curl that writes its output in slow chunks, so concurrent downloads
# overlap the way they do over a real network.
cat > "$fake_bin/curl" <<EOF
#!/usr/bin/env bash
out=""; url=""
while [ "\$#" -gt 0 ]; do
  case "\$1" in
    -o) out="\$2"; shift 2 ;;
    -*) shift ;;
    *) url="\$1"; shift ;;
  esac
done
case "\$url" in
  */checksums.txt) src="$tmp_dir/checksums.txt" ;;
  *) src="$asset_file" ;;
esac
: > "\$out"
size=\$(wc -c < "\$src")
chunk=\$(( size / 4 + 1 ))
for i in 0 1 2 3; do
  dd if="\$src" bs="\$chunk" skip="\$i" count=1 2>/dev/null >> "\$out"
  sleep 0.2
done
EOF

cat > "$fake_bin/tmux" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$fake_bin/curl" "$fake_bin/tmux"

make_plugin() {
  mkdir -p "$1/scripts"
  cp "$repo_root/scripts/ensure-binary.sh" "$1/scripts/"
  cp "$repo_root/VERSION" "$1/"
}

ensure() {
  XDG_CACHE_HOME="$cache" PATH="$fake_bin:$PATH" \
    bash "$1/scripts/ensure-binary.sh" "$1" 2>>"$tmp_dir/errors"
}

for round in 1 2 3; do
  plugins=()
  for name in tpm pmux; do
    plugin="$tmp_dir/round$round/$name"
    make_plugin "$plugin"
    plugins+=("$plugin")
  done
  rm -f "$cache"/checksums.txt.* "$cache"/*.tmp*

  ensure "${plugins[0]}" & first=$!
  sleep 0.1
  ensure "${plugins[1]}" & second=$!
  wait "$first" || true
  wait "$second" || true

  for plugin in "${plugins[@]}"; do
    if ! cmp -s "$asset_file" "$plugin/bin/tmux-ai-attn-sync"; then
      echo "round $round: $plugin got a corrupt or missing binary" >&2
      cat "$tmp_dir/errors" >&2 2>/dev/null || true
      exit 1
    fi
  done
done

# A failed download leaves no temporary files behind.
plugin="$tmp_dir/offline"
make_plugin "$plugin"
printf '#!/bin/sh\nexit 22\n' > "$fake_bin/curl"
ensure "$plugin" || true
leftovers="$(find "$cache" "$plugin" -name '.*.??????' -type f)"
if [ -n "$leftovers" ]; then
  echo "temporary files were left behind:" >&2
  printf '%s\n' "$leftovers" >&2
  exit 1
fi

echo "ensure-binary tests passed"
