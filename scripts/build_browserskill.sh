#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${1:-$repo_root/artifacts/browserskill}"
# Optional Docker-style target; native builds default to the host platform.
target_platform="${2:-}"
case "$target_platform" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo "Unsupported BrowserSkill target: $target_platform" >&2; exit 1 ;;
esac
# Build both ends from the same pinned protocol baseline.
source_commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["extension"]["source_commit"])' "$repo_root/scripts/browserskill-release.json")"
extension_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["extension"]["version"])' "$repo_root/scripts/browserskill-release.json")"
# Native builds only. Docker runs this stage on TARGETPLATFORM so C/Rust
# dependencies and the produced daemon match the final image architecture.
host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(uname -m)"
case "$host_arch" in arm64|aarch64) host_arch=arm64 ;; x86_64) host_arch=amd64 ;; esac
if [ -n "$target_platform" ] && [ "$target_platform" != "$host_os/$host_arch" ]; then
  echo "Run the BrowserSkill build on $target_platform (host is $host_os/$host_arch)" >&2
  exit 1
fi
if ! command -v cargo >/dev/null && [ -x "${CARGO_HOME:-$HOME/.cargo}/bin/cargo" ]; then
  export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
fi
command -v cargo >/dev/null || { echo "Rust/Cargo is required to build the matching BrowserSkill daemon" >&2; exit 1; }
daemon_commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["daemon"]["source_commit"])' "$repo_root/scripts/browserskill-release.json")"
[ "$daemon_commit" = "$source_commit" ] || { echo "BrowserSkill daemon/extension baseline mismatch" >&2; exit 1; }
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
build_dir="$(mktemp -d /tmp/weknora-bsk-build.XXXXXX)"
trap 'rm -rf "$build_dir"' EXIT

# Source repository. Defaults to upstream GitHub; override with
# BROWSERSKILL_REPO when GitHub is slow/unreachable from the build network,
# e.g. BROWSERSKILL_REPO=https://gh-proxy.com/https://github.com/Tencent/BrowserSkill.git
repo_url="${BROWSERSKILL_REPO:-https://github.com/Tencent/BrowserSkill.git}"
# Some networks break GitHub's HTTP/2 framing (curl 16 / "RPC failed");
# HTTP/1.1 is the safe default and can be overridden if needed.
git_http_version="${BROWSERSKILL_GIT_HTTP_VERSION:-HTTP/1.1}"
fetch_attempts="${BROWSERSKILL_FETCH_ATTEMPTS:-3}"

# Fetch only the pinned commit instead of cloning the whole history: the
# repository carries large asset branches, so a full clone is both slower and
# far more likely to hit a transient network error.
fetch_source() {
  local dest="$1" attempt=1
  while [ "$attempt" -le "$fetch_attempts" ]; do
    rm -rf "$dest"
    if git init -q "$dest" &&
       git -C "$dest" remote add origin "$repo_url" &&
       git -C "$dest" -c http.version="$git_http_version" \
         fetch --depth 1 origin "$source_commit" &&
       git -C "$dest" checkout -q --detach FETCH_HEAD; then
      return 0
    fi
    echo "BrowserSkill source fetch failed (attempt $attempt/$fetch_attempts) from $repo_url" >&2
    attempt=$((attempt + 1))
    [ "$attempt" -le "$fetch_attempts" ] && sleep $((attempt * 5))
  done
  return 1
}

if ! fetch_source "$build_dir/source"; then
  echo "Falling back to a full clone from $repo_url" >&2
  rm -rf "$build_dir/source"
  git -c http.version="$git_http_version" clone --no-checkout "$repo_url" "$build_dir/source"
  git -C "$build_dir/source" checkout --detach "$source_commit"
fi
(
  cd "$build_dir/source"
  npx --yes pnpm@10.17.0 install --frozen-lockfile
  npx --yes pnpm@10.17.0 ext:build:zip
)
cp "$build_dir/source/apps/extension/dist/browser-skillextension-${extension_version}-chrome.zip" "$output_dir/browser-skill-weknora-${extension_version}.zip"
cp "$build_dir/source/LICENSE" "$output_dir/BrowserSkill-LICENSE"

# Build the daemon from the same pinned source as the bundled extension.
cargo_target_dir="${CARGO_TARGET_DIR:-$build_dir/target}"
mkdir -p "$cargo_target_dir"
cargo_target_dir="$(cd "$cargo_target_dir" && pwd)"
(
  cd "$build_dir/source"
  # Use the installed toolchain; do not let the checkout's moving "stable"
  # override force a network update during each application build.
  RUSTUP_TOOLCHAIN="${RUSTUP_TOOLCHAIN:-stable}" cargo build --locked --release -p bsk --target-dir "$cargo_target_dir"
)
# Replace atomically: overwriting an executing inode can invalidate macOS code pages.
staged_binary="$(mktemp "$output_dir/.bsk-XXXXXX")"
install -m 755 "$cargo_target_dir/release/bsk" "$staged_binary"
mv -f "$staged_binary" "$output_dir/bsk"
echo "BrowserSkill $extension_version extension and daemon: $output_dir"
