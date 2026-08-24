package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/config"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/httpserver"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/pipeline"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/publisher"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("StellarView Indexer\n")
	fmt.Printf("  Network:    %s\n", cfg.Network)
	fmt.Printf("  RPC:        %s\n", cfg.RPCEndpoint)
	fmt.Printf("  Database:   %s\n", cfg.DatabaseURL)
	fmt.Printf("  Workers:    %d\n", cfg.WorkerCount)

	if len(os.Args) < 2 {
		fmt.Println("Usage: indexer <live|backfill|s3backfill|serve|analytics-backfill|migrate>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "live":
		if cfg.RPCEndpoint == "" {
			log.Fatal("RPC_ENDPOINT is required for live command")
		}
		runLive(cfg)
	case "backfill":
		if cfg.RPCEndpoint == "" {
			log.Fatal("RPC_ENDPOINT is required for backfill command")
		}
		runBackfill(cfg)
	case "s3backfill":
		runS3Backfill(cfg)
	case "serve":
		runServe(cfg)
	case "analytics-backfill":
		runAnalyticsBackfill(cfg)
	case "migrate":
		runMigrate(cfg.DatabaseURL)
	default:
		log.Fatalf("Unknown command: %s. Use: live, backfill, s3backfill, serve, analytics-backfill, migrate", os.Args[1])
	}
}

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight
// requests before the process gives up on them.
//
// It must exceed the server's write timeout so a request already in flight can
// finish rather than being cut off by the drain.
const shutdownTimeout = 20 * time.Second

func setupContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()
	return ctx, cancel
}

func initDeps(cfg *config.Config, passphrase string) (*store.PostgresStore, *source.RPCClient) {
	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	rpc := source.NewRPCClient(cfg.RPCEndpoint, passphrase)
	return db, rpc
}

func runLive(cfg *config.Config) {
	ctx, cancel := setupContext()
	defer cancel()

	passphrase, err := cfg.NetworkPassphrase()
	if err != nil {
		log.Fatalf("Failed to resolve network passphrase: %v", err)
	}

	db, rpc := initDeps(cfg, passphrase)
	defer db.Close()

	if cfg.ListenAddr() != "" {
		// The analytics API is deliberately not mounted here. It would share the
		// pipeline's connection pool, so a burst of dashboard queries could
		// exhaust it, stall ingestion writes, and trip the /healthz staleness
		// check into a restart. Run it as its own process with `serve`.
		srv := httpserver.New(cfg.ListenAddr(), httpserver.Options{
			DB:            db.DB(),
			ExposeMetrics: true,
		})
		srv.SetDomainReader(db)
		go func() {
			log.Printf("http server listening on %s (/metrics, /healthz, /v1/domains)", cfg.ListenAddr())
			if err := srv.Start(); err != nil {
				log.Printf("http server error: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer shutdownCancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("http server shutdown error: %v", err)
			}
		}()
	}

	p := pipeline.NewLivePipeline(rpc, db, passphrase, cfg.BatchSize, cfg.WorkerCount)
	p.SetRegistryContractIDs(cfg.RegistryContractIDs())

	// Attach Redis publisher if configured
	if cfg.RedisURL != "" {
		pub, err := publisher.NewRedisPublisher(cfg.RedisURL)
		if err != nil {
			log.Printf("Warning: Redis publisher unavailable: %v", err)
		} else {
			defer pub.Close()
			p.SetPublisher(pub)
			log.Println("Redis publisher attached")
		}
	}

	log.Println("Starting live ingestion...")
	if err := p.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Live pipeline failed: %v", err)
	}
	log.Println("Shutdown complete.")
}

// runServe starts the analytics read API without ingesting anything. This is
// the process the explorer points NEXT_PUBLIC_INDEXER_URL at, and the only one
// that serves those routes — see the comment in runLive for why ingestion does
// not.
func runServe(cfg *config.Config) {
	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	srv := httpserver.New(cfg.APIAddr, httpserver.Options{
		DB:             db.DB(),
		Analytics:      db,
		AllowedOrigins: cfg.APICORSOrigins,
	})

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("api server shutdown error: %v", err)
		}
	}()

	log.Printf("analytics API listening on %s (/api/v1/analytics, /healthz)", cfg.APIAddr)
	if err := srv.Start(); err != nil {
		log.Fatalf("API server failed: %v", err)
	}

	// Start returns as soon as the listener closes, which is the beginning of
	// the shutdown rather than the end of it. Waiting for the drain keeps the
	// deferred db.Close() from pulling the pool out from under requests that
	// are still being served.
	<-drained
	log.Println("Shutdown complete.")
}

func runBackfill(cfg *config.Config) {
	startLedger, endLedger := parseBackfillFlags()

	ctx, cancel := setupContext()
	defer cancel()

	passphrase, err := cfg.NetworkPassphrase()
	if err != nil {
		log.Fatalf("Failed to resolve network passphrase: %v", err)
	}

	db, rpc := initDeps(cfg, passphrase)
	defer db.Close()

	p := pipeline.NewBackfillPipeline(rpc, db, passphrase, cfg.BatchSize, cfg.WorkerCount)
	p.SetRegistryContractIDs(cfg.RegistryContractIDs())

	log.Printf("Starting backfill from ledger %d to %d...", startLedger, endLedger)
	if err := p.Run(ctx, startLedger, endLedger); err != nil && err != context.Canceled {
		log.Fatalf("Backfill failed: %v", err)
	}
	log.Println("Backfill complete.")
}

func runS3Backfill(cfg *config.Config) {
	startLedger, endLedger := parseBackfillFlags()

	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	p := pipeline.NewS3BackfillPipeline(db, cfg.WorkerCount)
	p.SetRegistryContractIDs(cfg.RegistryContractIDs())

	log.Printf("Starting S3 data lake backfill from ledger %d to %d...", startLedger, endLedger)
	if err := p.Run(ctx, startLedger, endLedger); err != nil && err != context.Canceled {
		log.Fatalf("S3 backfill failed: %v", err)
	}
	log.Println("S3 backfill complete.")
}

// runAnalyticsBackfill populates the analytics continuous aggregates from data
// already in the database. The migration creates them empty so it stays instant
// on a populated database; this is the one-off that fills them in.
//
// Re-running is safe and cheap: TimescaleDB commits each batch separately and
// skips buckets that are already materialized, so an interrupted run resumes.
func runAnalyticsBackfill(cfg *config.Config) {
	from, to := parseAnalyticsWindowFlags()
	if to.IsZero() {
		// An open upper bound would materialize the bucket currently being
		// written and advance the watermark past it. Watermarks never move
		// back, so that bucket would then be frozen at its partial value until
		// a refresh policy reached it again — up to ten days for the weekly
		// aggregate. Ending at the present lets each aggregate snap down to its
		// last completed bucket instead.
		to = time.Now().UTC()
	}

	ctx, cancel := setupContext()
	defer cancel()

	db, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	log.Println("Refreshing analytics aggregates...")
	results, err := db.RefreshAnalyticsAggregates(ctx, from, to)

	// Report whatever completed before reacting to a failure, so a partial run
	// still tells the operator where it got to.
	for _, r := range results {
		if r.Skipped {
			log.Printf("  %-38s skipped: window holds no complete bucket", r.Aggregate)
			continue
		}
		log.Printf("  %-38s refreshed in %s", r.Aggregate, r.Duration.Round(time.Millisecond))
	}

	// Any failure, interruption included, leaves later aggregates untouched.
	// Reporting success would let a deploy step gated on the exit status start
	// serving from aggregates that were never populated.
	if err != nil {
		log.Fatalf("Analytics backfill stopped after %d of %d aggregates: %v",
			len(results), store.AnalyticsAggregateCount(), err)
	}

	// Every aggregate being skipped means the window was narrower than any
	// bucket, so nothing was materialized. Exiting zero there would let a
	// deploy step gated on this command proceed to serve empty aggregates.
	refreshed := 0
	for _, r := range results {
		if !r.Skipped {
			refreshed++
		}
	}
	if refreshed == 0 {
		log.Fatalf("Analytics backfill refreshed nothing: the window holds no complete bucket for any of the %d aggregates",
			store.AnalyticsAggregateCount())
	}

	log.Printf("Analytics backfill complete: %d of %d aggregates refreshed.",
		refreshed, store.AnalyticsAggregateCount())
}

// parseAnalyticsWindowFlags reads the optional --from/--to RFC 3339 bounds,
// in either the "--from X" or "--from=X" form. Omitting --from reaches as far
// back as the data goes; omitting --to ends at the present.
//
// Anything unrecognised is rejected rather than ignored. Silently skipping a
// typo would turn a scoped repair into a refresh of all history, which on a
// populated database is hours of I/O that cannot be undone.
func parseAnalyticsWindowFlags() (from, to time.Time) {
	from, to, err := parseAnalyticsWindow(os.Args[2:])
	if err != nil {
		log.Fatalf("%v\nUsage: indexer analytics-backfill [--from RFC3339] [--to RFC3339]", err)
	}
	return from, to
}

// parseAnalyticsWindow reads the optional --from/--to RFC 3339 bounds from the
// arguments following the subcommand, in either the "--from X" or "--from=X"
// form. A zero bound means unbounded in that direction.
func parseAnalyticsWindow(args []string) (from, to time.Time, err error) {
	parse := func(flag, raw string) (time.Time, error) {
		ts, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return time.Time{}, fmt.Errorf("invalid %s value %q: expected RFC 3339, e.g. 2026-01-01T00:00:00Z", flag, raw)
		}
		return ts.UTC(), nil
	}

	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if !inline {
			if i+1 >= len(args) {
				return time.Time{}, time.Time{}, fmt.Errorf("missing value for %s", args[i])
			}
			i++
			value = args[i]
		}

		switch flag {
		case "--from":
			from, err = parse("--from", value)
		case "--to":
			to, err = parse("--to", value)
		default:
			return time.Time{}, time.Time{}, fmt.Errorf("unknown flag %q", flag)
		}
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("--from (%s) must be before --to (%s)",
			from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	return from, to, nil
}

func parseBackfillFlags() (uint32, uint32) {
	var startLedger, endLedger uint32

	for i := 2; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--start":
			n, err := strconv.ParseUint(os.Args[i+1], 10, 32)
			if err != nil {
				log.Fatalf("Invalid --start value: %v", err)
			}
			startLedger = uint32(n)
		case "--end":
			n, err := strconv.ParseUint(os.Args[i+1], 10, 32)
			if err != nil {
				log.Fatalf("Invalid --end value: %v", err)
			}
			endLedger = uint32(n)
		}
	}

	if startLedger == 0 || endLedger == 0 {
		log.Fatal("Usage: indexer backfill --start <ledger> --end <ledger>")
	}

	return startLedger, endLedger
}
