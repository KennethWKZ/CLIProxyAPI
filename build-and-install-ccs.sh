#!/usr/bin/env bash
#
# build-and-install-ccs.sh
#
# Build a release (non-debug) cli-proxy-api binary from source and install it
# over the CCS-managed CLIProxy binary, after first refreshing CCS's upstream
# ("origin") CLIProxy so its scaffolding (.version, caches, static assets) is
# current.
#
# CGO note: internal/pluginhost uses cgo (import "C"), so CGO_ENABLED=1 is
# REQUIRED. Building with CGO_ENABLED=0 fails to compile.
#
# Flow:
#   1. Build cli-proxy-api with stripped/trimmed release flags.
#   2. ccs cliproxy --latest   (refresh upstream origin binary + scaffolding).
#   3. Atomically overlay our custom binary on top, keeping a timestamped
#      backup of the origin binary CCS just installed.
#
# The install target lives inside CCS's managed bin dir, so a later
# 'ccs cliproxy --latest/--update' (or CCS auto-update) overwrites the custom
# binary with upstream again. Re-run this script to re-apply.
#
# Usage:
#   ./build-and-install-ccs.sh [options]
#
# Options:
#   --restart        Restart CLIProxy after install so the custom binary goes
#                    live now (drops any active sessions).
#   --skip-latest    Skip the 'ccs cliproxy --latest' upstream refresh.
#   --backend <b>    Target backend: original (default) | plus.
#   --build-only     Build only; do not touch CCS.
#   -h, --help       Show this help.
#
# Env overrides:
#   VERSION            Version string to embed
#                      (default: "$(git describe --tags --always --dirty)-custom").
#   CCS_CLIPROXY_DIR   CCS cliproxy dir (default: $HOME/.ccs/cliproxy).

set -euo pipefail

BACKEND="original"
DO_RESTART=0
SKIP_LATEST=0
BUILD_ONLY=0

usage() { grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --restart)     DO_RESTART=1; shift ;;
    --skip-latest) SKIP_LATEST=1; shift ;;
    --build-only)  BUILD_ONLY=1; shift ;;
    --backend)     BACKEND="${2:?--backend requires a value}"; shift 2 ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "Error: unknown option '$1'" >&2; exit 1 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

CCS_CLIPROXY_DIR="${CCS_CLIPROXY_DIR:-$HOME/.ccs/cliproxy}"
BIN_DIR="$CCS_CLIPROXY_DIR/bin/$BACKEND"
case "$BACKEND" in
  original) BIN_NAME="cli-proxy-api" ;;
  plus)     BIN_NAME="cli-proxy-api-plus" ;;
  *) echo "Error: unsupported backend '$BACKEND' (use: original | plus)" >&2; exit 1 ;;
esac
TARGET_BIN="$BIN_DIR/$BIN_NAME"

# --- Preflight: required tools ---
NEED=(go git)
[[ "$BUILD_ONLY" -eq 1 ]] || NEED+=(ccs)
for tool in "${NEED[@]}"; do
  command -v "$tool" >/dev/null 2>&1 || { echo "Error: '$tool' not found in PATH" >&2; exit 1; }
done

# --- Step 1: Build (non-debug / release) ---
VERSION="${VERSION:-$(git describe --tags --always --dirty)-custom}"
COMMIT="$(git rev-parse --short HEAD)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OUT="$REPO_ROOT/cli-proxy-api"

echo "==> Building $BIN_NAME (release, CGO_ENABLED=1)"
echo "    Version:    $VERSION"
echo "    Commit:     $COMMIT"
echo "    Build date: $BUILD_DATE"

CGO_ENABLED=1 go build -trimpath \
  -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
  -o "$OUT" ./cmd/server
echo "    Built: $OUT"

if [[ "$BUILD_ONLY" -eq 1 ]]; then
  echo "==> Build-only mode; skipping CCS install."
  exit 0
fi

# --- Step 2: Refresh upstream origin via CCS ---
if [[ "$SKIP_LATEST" -eq 0 ]]; then
  echo "==> Refreshing upstream CLIProxy: ccs cliproxy --latest --backend $BACKEND"
  ccs cliproxy --latest --backend "$BACKEND"
else
  echo "==> Skipping upstream refresh (--skip-latest)"
fi

# --- Step 3: Overlay custom binary (atomic; safe while proxy runs) ---
if [[ ! -d "$BIN_DIR" ]]; then
  echo "Error: CCS bin dir not found: $BIN_DIR" >&2
  echo "       Run 'ccs cliproxy --latest' once first, or set CCS_CLIPROXY_DIR." >&2
  exit 1
fi

WAS_RUNNING=0
if ccs cliproxy status 2>/dev/null | grep -qiE 'Status:[[:space:]]*Running'; then
  WAS_RUNNING=1
fi

if [[ -f "$TARGET_BIN" ]]; then
  BACKUP="$TARGET_BIN.origin.$(date -u +%Y%m%d%H%M%S).bak"
  cp -p "$TARGET_BIN" "$BACKUP"
  echo "==> Backed up origin binary: $BACKUP"
fi

TMP="$BIN_DIR/.$BIN_NAME.custom.$$"
cp -f "$OUT" "$TMP"
chmod +x "$TMP"
mv -f "$TMP" "$TARGET_BIN"   # atomic rename: no "Text file busy" if proxy is running
echo "==> Installed custom binary: $TARGET_BIN"

# --- Verify (side-effect free: empty bootstrap config, -h exits at flag parse) ---
echo "==> Installed binary reports:"
"$TARGET_BIN" -config /dev/null -h 2>/dev/null | head -1 || true

# --- Cut over ---
if [[ "$DO_RESTART" -eq 1 ]]; then
  echo "==> Restarting CLIProxy to activate custom binary"
  ccs cliproxy restart
elif [[ "$WAS_RUNNING" -eq 1 ]]; then
  echo "==> CLIProxy still runs the previous binary. Activate the custom build with:"
  echo "      ccs cliproxy restart"
else
  echo "==> Start CLIProxy with: ccs cliproxy start"
fi
