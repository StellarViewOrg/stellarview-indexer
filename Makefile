.PHONY: build test lint fmt run-live run-backfill migrate run-api builder-image clean

build:
	go build -o bin/indexer ./cmd/indexer

test:
	go test ./... -v

fmt:
	gofmt -w .

lint:
	go vet ./...

run-live: build
	./bin/indexer live

run-backfill: build
	./bin/indexer backfill

migrate: build
	./bin/indexer migrate

run-api: build
	./bin/indexer api

builder-image:
	docker build -t stellarview/soroban-builder:latest infra/docker/builder

clean:
	rm -rf bin/
