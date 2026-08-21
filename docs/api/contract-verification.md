# Contract Verification API — v1 (FROZEN)

This document freezes the v1 contract-verification API surface. The companion
explorer (StellarViewOrg/stellarview-explorer#55) may build against these
shapes; breaking changes require a new version prefix (`/api/v2/...`).

Base URL: `VERIFY_API_ADDR` (default `:8080`), served by `./bin/indexer api`.

All request/response bodies are JSON unless noted. Timestamps are RFC 3339
UTC. All endpoints return `application/json` except build logs and source
file content.

---

## Concepts

- A **verification** is one submission: source (uploaded archive or git
  ref + commit) plus build metadata for a deployed contract.
- The indexer reproduces the build inside a sandboxed builder container
  (`--network=none`, read-only rootfs, dropped capabilities, CPU/memory/PID
  limits) and compares the SHA-256 of the produced WASM with the on-chain
  `wasm_hash` stored in `contract_code`.
- On success, the source tree is stored **keyed by wasm_hash**. Contracts
  sharing identical bytecode share one verification record.
- SEP-41 Stellar Asset Contracts (SACs) have no WASM and cannot be verified
  (`sac_unsupported`).

## Verification lifecycle

```
queued ──> building ──> verified    (hash match)
                  └────> mismatch   (build ok, hash differs)
                  └────> failed     (invalid source, build error, timeout)
```

`match` is `null` until the build finishes, then `true`/`false`.
`wasm_hash` is the hash of the built artifact (set on verified/mismatch).

---

## Endpoints

### POST /api/v1/contracts/{contract_id}/verifications

Submit a verification. Returns `202 Accepted` immediately; builds run
asynchronously.

`contract_id` must be a valid Stellar contract address (`C...`).

**Variant A — archive upload** (`multipart/form-data`):

| Field | Type | Required | Notes |
|---|---|---|---|
| `archive` | file | yes | `.zip`, `.tar.gz`, `.tgz`, or `.tar`; max `VERIFY_MAX_ARCHIVE_MB` (default 20 MB); must contain a `Cargo.toml` at the root, inside a single top-level directory, or in `build_crate_dir` |
| `network` | string | no | must match the indexer's `NETWORK` if sent |
| `toolchain_rust` | string | no | requested Rust version (informational) |
| `toolchain_soroban_sdk` | string | no | requested soroban-sdk version (informational) |
| `toolchain_stellar_cli` | string | no | requested stellar-cli version (informational) |
| `build_profile` | string | no | only `release` supported (default) |
| `build_package` | string | no | cargo package name in a workspace |
| `build_crate_dir` | string | no | relative dir containing the crate's `Cargo.toml` |
| `build_features` | string | no | comma-separated feature list (max 16) |
| `build_no_default_features` | bool | no | `"true"` to disable default features |
| `build_flags` | string | no | comma-separated extra cargo flags (max 16, restricted charset) |

```sh
curl -X POST http://localhost:8080/api/v1/contracts/CBQH...6RNV/verifications \
  -F archive=@contract.tar.gz \
  -F network=testnet \
  -F toolchain_rust=1.85.0
```

**Variant B — git source** (`application/json`):

```json
{
  "source": {
    "type": "git",
    "repository": "https://github.com/org/repo",
    "reference": "main",
    "commit": "40-char full hex SHA (required)"
  },
  "network": "testnet",
  "toolchain": { "rust": "1.85.0", "soroban_sdk": "21.5.0", "stellar_cli": "22.0.8" },
  "build": {
    "profile": "release",
    "package": "hello_world",
    "crate_dir": "contracts/hello_world",
    "features": ["audit"],
    "no_default_features": false,
    "flags": []
  }
}
```

Only `https://` repositories without embedded credentials are accepted;
the commit is mandatory so builds are reproducible.

**Response `202`:**

```json
{
  "id": "6f0a8b1e-...-...",
  "status": "queued",
  "status_url": "/api/v1/verifications/6f0a8b1e-...-...",
  "created_at": "2026-08-21T12:00:00Z"
}
```

**Error envelope (all endpoints):**

```json
{ "error": { "code": "invalid_request", "message": "..." } }
```

| HTTP | code | meaning |
|---|---|---|
| 400 | `invalid_request` | malformed submission, bad paths/flags, traversal attempts |
| 404 | `unknown_contract` | contract_id not present in the indexed chain data |
| 404 | `not_found` | unknown verification id / unverified wasm_hash resource |
| 413 | `invalid_request` | upload exceeds size limit |
| 415 | `invalid_request` | wrong Content-Type |
| 422 | `sac_unsupported` | target is a Stellar Asset Contract (no WASM) |
| 429 | `rate_limited` | per-IP rate limit exceeded (`Retry-After` header set) |
| 500 | `internal` | unexpected server error (details logged, not returned) |
| 503 | `queue_full` | build queue at capacity; resubmit later |

---

### GET /api/v1/verifications/{id}

Poll verification status.

```json
{
  "id": "6f0a8b1e-...",
  "contract_id": "CBQH...6RNV",
  "network": "testnet",
  "status": "verified",
  "reason": "",
  "match": true,
  "wasm_hash": "aa03...",
  "expected_wasm_hash": "aa03...",
  "source": {
    "type": "archive",
    "name": "contract.tar.gz",
    "sha256": "bb12..."
  },
  "toolchain_requested": { "rust": "1.85.0" },
  "toolchain": { "rust": "1.85.0", "stellar_cli": "22.0.8", "soroban_sdk": "21.5.0", "target": "wasm32-unknown-unknown" },
  "build": { "profile": "release", "package": "", "crate_dir": "", "features": [], "no_default_features": false, "flags": [] },
  "build_logs_url": "/api/v1/verifications/6f0a8b1e-.../logs",
  "created_at": "2026-08-21T12:00:00Z",
  "started_at": "2026-08-21T12:00:02Z",
  "completed_at": "2026-08-21T12:01:10Z"
}
```

Notes:
- `source` for git submissions is
  `{"type":"git","repository":"https://...","reference":"main","commit":"..."}`.
- `reason` is empty when absent; on `mismatch` it reads e.g.
  `"built WASM hash <x> does not match on-chain hash <y>"`.
- `toolchain` is the toolchain actually used by the builder (null until the
  build completes).
- `started_at`/`completed_at` are omitted until set.

### GET /api/v1/verifications/{id}/logs

Raw build logs as `text/plain`. Empty body until the build finishes; logs are
stored for every terminal outcome including mismatches.

### GET /api/v1/wasm/{wasm_hash}/verification

Is this bytecode verified? `wasm_hash` is 64 lowercase hex chars.

Verified:

```json
{
  "wasm_hash": "aa03...",
  "verified": true,
  "verification_id": "6f0a8b1e-...",
  "contract_id": "CBQH...6RNV",
  "network": "testnet",
  "toolchain": { "...": "..." },
  "build": { "...": "..." },
  "source": { "...": "..." },
  "file_count": 12,
  "total_size": 45678,
  "verified_at": "2026-08-21T12:01:10Z"
}
```

Not verified (still `200` — lets UIs render an "Unverified" badge without
error handling):

```json
{ "wasm_hash": "aa03...", "verified": false }
```

### GET /api/v1/wasm/{wasm_hash}/source/tree

```json
{
  "wasm_hash": "aa03...",
  "verified": true,
  "files": [
    { "path": "Cargo.toml", "size": 331 },
    { "path": "src/lib.rs", "size": 1204 }
  ]
}
```

Files are sorted by path. `404 not_found` when the hash has no verified
source.

### GET /api/v1/wasm/{wasm_hash}/source/file?path=src/lib.rs

Returns raw file content. `Content-Type` is `text/plain; charset=utf-8`,
`application/json`, or `application/octet-stream`; `X-File-Path` echoes the
canonical path. Relative `path` only — `..` segments are rejected with
`400 invalid_request`.

---

## Abuse protection

- Per-IP token bucket: `VERIFY_RATE_RPS` (default 1/s) with burst
  `VERIFY_RATE_BURST` (default 5).
- Bounded queue (`VERIFY_QUEUE_SIZE`, default 16) → `503 queue_full` when full.
- Max parallel sandboxed builds: `VERIFY_BUILD_CONCURRENCY` (default 2).
- Upload cap `VERIFY_MAX_ARCHIVE_MB`; extraction caps: total uncompressed
  `VERIFY_MAX_EXTRACTED_MB` (default 100 MB), 8 MB per file, 4096 files,
  compression ratio ≤ 150×, depth ≤ 24.
- Archives containing symlinks/hardlinks/devices/fifos, absolute paths, or
  `..` components are rejected outright.
- Build sandbox: `docker run --network=none --read-only --cap-drop=ALL
  --security-opt no-new-privileges --pids-limit 256 --memory 2g --cpus 2`
  with tmpfs scratch mounts only; runs as uid 1000.

## Builder image

The sandbox image is built from `infra/docker/builder/Dockerfile`
(Rust + stellar-cli + `wasm32-unknown-unknown`, offline cargo registry cache)
and configured via `VERIFY_BUILDER_IMAGE`
(default `stellarview/soroban-builder:latest`). Build it with
`make builder-image`.
