package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
)

type config struct {
	StartLedger uint32 `json:"startLedger"`
	EndLedger   uint32 `json:"endLedger"`
	Manifest    string `json:"manifest"`
}

func main() {
	path := flag.String("config", "benchmark/config/pubnet-range.json", "benchmark configuration")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil {
		panic(err)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		panic(err)
	}
	if c.StartLedger == 0 || c.EndLedger < c.StartLedger {
		panic("invalid fixed range")
	}
	ds, err := source.NewAnonymousPubnetDataStore(context.Background())
	if err != nil {
		panic(err)
	}
	schema := source.PubnetDataLakeConfig().Schema
	backend, err := ledgerbackend.NewBufferedStorageBackend(ingest.DefaultBufferedStorageBackendConfig(schema.LedgersPerFile), ds, schema)
	if err != nil {
		panic(err)
	}
	defer backend.Close()
	if err := backend.PrepareRange(context.Background(), ledgerbackend.BoundedRange(c.StartLedger, c.EndLedger)); err != nil {
		panic(err)
	}
	for seq := c.StartLedger; seq <= c.EndLedger; seq++ {
		if _, err := backend.GetLedger(context.Background(), seq); err != nil {
			panic(fmt.Errorf("ledger %d: %w", seq, err))
		}
	}
	fmt.Printf("validated %d ledgers from the SDK S3 datastore; object names are SDK-managed\n", c.EndLedger-c.StartLedger+1)
}
