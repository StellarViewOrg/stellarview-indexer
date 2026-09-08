# Contract source verification API

Frozen contract for submitting and reading back reproducible-build source
verification (issue #36). Explorer clients should integrate against this
shape.

Base path is the indexer HTTP server (`HTTP_ADDR`, or `METRICS_ADDR` if
`HTTP_ADDR` is unset) — the same listener that serves `/v1/domains`.

## Status is currently always `pending`

Submitting always succeeds (subject to validation) and records a `pending`
row. The sandboxed reproducible-build pipeline that actually compiles the
source and compares the resulting wasm hash against the indexed
`contract_code.wasm_hash` is tracked as follow-up work and has not landed
yet. Until it does, every submission stays `pending` — clients must render
that as "not yet verified", never as a mismatch or failure.

`status` is one of:

- `pending` — submitted, awaiting the build pipeline.
- `verified` — the rebuilt wasm hash matched.
- `mismatch` — the rebuilt wasm hash did not match.
- `failed` — the build itself could not complete.

## Endpoints

### Submit source for verification

`POST /v1/verify`

```json
{
  "contractId": "C...",
  "network": "testnet",
  "repositoryUrl": "https://github.com/org/repo",
  "gitRef": "main",
  "gitCommit": "abc123",
  "rustVersion": "1.79.0",
  "sorobanSdkVersion": "21.0.0",
  "buildProfile": {},
  "files": {
    "src/lib.rs": "...",
    "Cargo.toml": "..."
  }
}
```

`contractId` must resolve to a wasm_hash the indexer has already observed
on-chain; `files` is a flat map of relative path to source content (at
least one entry). Returns `202` with the created verification record shown
below, keyed by `wasmHash` — a second submission against a wasm_hash
already indexed under a different `contractId` is stored under the same
`wasmHash`, since identical bytecode is one verification subject regardless
of how many contract IDs deploy it.

Limits, rejected as `400`:

| Field | Limit |
| --- | --- |
| `contractId` | 56 chars |
| `network` | 16 chars |
| `repositoryUrl` | 1024 chars |
| `gitRef` | 256 chars |
| `gitCommit` | 64 chars |
| `rustVersion` / `sorobanSdkVersion` | 64 chars |
| files per submission | 500 |
| bytes per file | 1 MiB |
| bytes per submission (all files) | 10 MiB |

File paths must be relative and cannot contain `..` segments, a leading
`/` or `\`, or a Windows drive prefix (`C:...`) — anything else is rejected
as `400`.

This endpoint has no auth and is rate-limited per source IP to 10 requests
per minute (`429` once exceeded). It does not check that the submitter
controls the contract's deployer key: anyone who knows a `contractId` can
submit source for it.

### Look up by contract ID

`GET /v1/verify/contract/{contractId}`

### Look up by wasm hash

`GET /v1/verify/wasm/{wasmHash}`

Both return the verification record for that wasm_hash's most relevant
submission, or `{"wasmHash", "verified": false, "status": "unverified"}`
with `200` if none exists yet. "Most relevant" means: a `verified` record,
if one exists, regardless of how many later submissions have been made
against the same wasm_hash; otherwise the most recently submitted record.
This is deliberate — since submission has no auth, a `verified` record is
never displaced by a newer resubmission of any other status.

```json
{
  "id": 1,
  "wasmHash": "...",
  "contractId": "C...",
  "network": "testnet",
  "repositoryUrl": "https://github.com/org/repo",
  "gitRef": "main",
  "gitCommit": "abc123",
  "rustVersion": "1.79.0",
  "sorobanSdkVersion": "21.0.0",
  "status": "pending",
  "computedWasmHash": "",
  "failureReason": "",
  "submittedAt": "2026-01-01T00:00:00.000Z",
  "completedAt": ""
}
```

Optional fields are omitted (never `null`) when unset.

### Source tree

`GET /v1/verify/wasm/{wasmHash}/source`

Lists the file tree (path and size, no content) belonging to that
wasm_hash's most relevant submission (same precedence as above).

### Source file

`GET /v1/verify/wasm/{wasmHash}/source/{path...}`

Returns one file's content from that same submission. `404` if the
wasm_hash has no verification or `path` was not part of it.
