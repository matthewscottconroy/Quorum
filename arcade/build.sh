#!/usr/bin/env bash
# Builds the arcade cartridge to WebAssembly and installs it into web/arcade/
# (which is embedded into the Go binary at build time via //go:embed web).
#
# Requirements:
#   rustup target add wasm32-unknown-unknown
#   cargo install wasm-bindgen-cli --version 0.2.126   (must match Cargo.toml pin)
#   (optional) wasm-opt from binaryen, for a ~15-20% smaller download
#
# Usage: ./build.sh
set -euo pipefail
cd "$(dirname "$0")"

OUT="../web/arcade"

echo "==> cargo build (release, wasm32)"
cargo build -p arcade --release --target wasm32-unknown-unknown

echo "==> wasm-bindgen"
mkdir -p "$OUT"
wasm-bindgen --target web --no-typescript \
  --out-dir "$OUT" --out-name arcade \
  target/wasm32-unknown-unknown/release/arcade.wasm

if command -v wasm-opt >/dev/null 2>&1; then
  echo "==> wasm-opt -Os"
  wasm-opt -Os -o "$OUT/arcade_bg.wasm.opt" "$OUT/arcade_bg.wasm"
  mv "$OUT/arcade_bg.wasm.opt" "$OUT/arcade_bg.wasm"
else
  echo "==> wasm-opt not found; skipping (optional)"
fi

ls -lh "$OUT"
echo "done — web/arcade/ is gitignored; rebuild the Go binary (make build) to embed it"
