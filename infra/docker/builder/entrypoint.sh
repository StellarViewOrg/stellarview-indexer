#!/bin/sh
set -eu

export CARGO_TARGET_DIR=/build/target
export CARGO_NET_OFFLINE=true

mkdir -p /out /build

RUST_VER="${RUST_VERSION:-unknown}"
CLI_VER="${STELLAR_CLI_VERSION:-unknown}"
SDK_VER=""
if [ -f Cargo.lock ]; then
    SDK_VER=$(grep -A1 '^name = "soroban-sdk"' Cargo.lock | grep '^version' | head -1 | cut -d'"' -f2 || true)
fi

printf '{"rust":"%s","stellar_cli":"%s","soroban_sdk":"%s","target":"wasm32-unknown-unknown"}\n' \
    "$RUST_VER" "$CLI_VER" "$SDK_VER" > /out/toolchain.json || true

cd /src
if [ -n "${BUILD_CRATE_DIR:-}" ]; then
    cd "${BUILD_CRATE_DIR}"
fi

set -- build --profile release --out-dir /out
if [ -n "${BUILD_PACKAGE:-}" ]; then
    set -- "$@" --package "${BUILD_PACKAGE}"
fi
if [ -n "${BUILD_FEATURES:-}" ]; then
    set -- "$@" --features "${BUILD_FEATURES}"
fi
if [ "${BUILD_NO_DEFAULT_FEATURES:-false}" = "true" ]; then
    set -- "$@" --no-default-features
fi
if [ -n "${BUILD_FLAGS:-}" ]; then
    for flag in ${BUILD_FLAGS}; do
        set -- "$@" "$flag"
    done
fi

stellar contract build "$@"

WASM_PATH=""
for f in /out/*.wasm; do
    if [ -e "$f" ] && [ "$f" != "/out/contract.wasm" ]; then
        WASM_PATH="$f"
        break
    fi
done
if [ -z "$WASM_PATH" ] && [ ! -e /out/contract.wasm ]; then
    echo "error: no .wasm produced by build" >&2
    exit 1
fi
if [ -n "$WASM_PATH" ]; then
    mv "$WASM_PATH" /out/contract.wasm
fi
