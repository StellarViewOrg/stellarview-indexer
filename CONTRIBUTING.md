# Contributing to StellarView Indexer

Thanks for contributing! StellarView Indexer is a Go service that ingests Stellar
network data into PostgreSQL/TimescaleDB and publishes real-time events via Redis.

## Prerequisites

- Go 1.25+ (managed via [asdf](https://asdf-vm.com/); see `.tool-versions`)
- Docker + Docker Compose (for local PostgreSQL/TimescaleDB, Redis, Typesense)

## Local setup

```bash
# start local infrastructure (see infra/docker-compose.yml)
docker compose -f infra/docker-compose.yml up -d

# build, apply migrations, and run the live pipeline
make build
make migrate
make run-live        # or: make run-backfill
```

Configuration is read from environment variables (network, RPC endpoint, database
URL, batch/worker sizes). See `README.md` for the full list.

## Building, testing, formatting

```bash
make build        # go build -o bin/indexer ./cmd/indexer
make fmt          # gofmt -w .
make lint         # go vet ./...
make test         # full suite (needs live network + a running database)
```

**Match CI locally** with the hermetic suite (this is what CI runs):

```bash
go test -race -short ./...
```

Tests that reach the live network or a database are gated behind `testing.Short()`,
so `-short` runs a deterministic, offline subset. When adding tests, prefer hermetic
tests with recorded fixtures; if a test genuinely needs the network, gate it behind
`testing.Short()`.

## Pull requests

- Branch off `main` and open a PR against `main` (direct pushes to `main` are disabled).
- Keep each PR focused on one change; reference the issue it resolves with `Closes #<n>`.
- CI (**Build, Lint & Test**) must pass before a PR can merge.
- PRs are merged with **squash and merge**; write a clear, imperative commit title.
- Run `make fmt` and `go vet ./...` before pushing.

## Code style

- Standard Go formatting (`gofmt`). CI enforces formatting and `go vet`.
