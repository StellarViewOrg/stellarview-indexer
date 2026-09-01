.PHONY: build test lint fmt run-live run-backfill run-serve migrate clean

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

run-serve: build
	./bin/indexer serve

migrate: build
	./bin/indexer migrate

clean:
	rm -rf bin/

benchmark-up:
	./benchmark/scripts/run.sh up

benchmark-prepare:
	./benchmark/scripts/run.sh prepare

benchmark-load-postgres:
	./benchmark/scripts/run.sh load-postgres

benchmark-load-clickhouse:
	./benchmark/scripts/run.sh load-clickhouse

benchmark-query:
	./benchmark/scripts/run.sh query

benchmark-storage:
	./benchmark/scripts/run.sh storage

benchmark-run:
	./benchmark/scripts/run.sh run

benchmark-report:
	./benchmark/scripts/run.sh report

benchmark-clean:
	./benchmark/scripts/run.sh clean

benchmark-test:
	./benchmark/scripts/run.sh validate
