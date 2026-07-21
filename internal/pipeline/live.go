package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/metrics"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/transform"
)

// Publisher is an optional interface for publishing new data after DB writes.
type Publisher interface {
	PublishLedger(ctx context.Context, ledger *store.Ledger) error
	PublishTransactions(ctx context.Context, txs []store.Transaction) error
}

// LivePipeline polls the Stellar RPC for new ledgers and ingests them.
type LivePipeline struct {
	rpc               *source.RPCClient
	store             *store.PostgresStore
	publisher         Publisher
	networkPassphrase string
	batchSize         int
}

func NewLivePipeline(rpc *source.RPCClient, store *store.PostgresStore, networkPassphrase string, batchSize int) *LivePipeline {
	return &LivePipeline{
		rpc:               rpc,
		store:             store,
		networkPassphrase: networkPassphrase,
		batchSize:         batchSize,
	}
}

func (p *LivePipeline) SetPublisher(pub Publisher) {
	p.publisher = pub
}

// Run starts the live ingestion loop. It blocks until the context is cancelled.
func (p *LivePipeline) Run(ctx context.Context) error {
	log.Println("live pipeline: starting")

	gapTicker := time.NewTicker(5 * time.Minute)
	defer gapTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("live pipeline: stopping")
			return ctx.Err()
		case <-gapTicker.C:
			p.detectAndFillGaps(ctx)
		default:
		}

		ingested, err := p.ingestNewLedgers(ctx)
		if err != nil {
			log.Printf("live pipeline: ingestion error: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}

		if ingested == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
	}
}

func (p *LivePipeline) ingestNewLedgers(ctx context.Context) (int, error) {
	latest, err := p.rpc.GetLatestLedger(ctx)
	if err != nil {
		metrics.RPCErrors.Inc()
		return 0, fmt.Errorf("getLatestLedger: %w", err)
	}

	lastIngested, err := p.store.GetLastIngestedLedger(ctx)
	if err != nil {
		metrics.DBErrors.Inc()
		return 0, fmt.Errorf("getLastIngestedLedger: %w", err)
	}

	// If first run, start from latest ledger (don't try to catch up all history)
	if lastIngested == 0 {
		lastIngested = latest.Sequence - 1
		log.Printf("live pipeline: first run, starting from ledger %d", lastIngested+1)
	}

	metrics.IngestionLagLedgers.Set(float64(latest.Sequence - lastIngested))

	if latest.Sequence <= lastIngested {
		return 0, nil
	}

	gap := latest.Sequence - lastIngested
	log.Printf("live pipeline: latest=%d last_ingested=%d gap=%d", latest.Sequence, lastIngested, gap)

	// Process in batches
	totalIngested := 0
	cursor := lastIngested + 1

	for cursor <= latest.Sequence {
		remaining := int(latest.Sequence - cursor + 1)
		limit := p.batchSize
		if remaining < limit {
			limit = remaining
		}

		count, err := p.processLedgerBatch(ctx, cursor, limit)
		if err != nil {
			return totalIngested, fmt.Errorf("processLedgerBatch at %d: %w", cursor, err)
		}

		cursor += uint32(count)
		totalIngested += count
	}

	return totalIngested, nil
}

func (p *LivePipeline) processLedgerBatch(ctx context.Context, startLedger uint32, limit int) (int, error) {
	// Fetch ledgers
	ledgerResult, err := p.rpc.GetLedgers(ctx, source.GetLedgersParams{
		StartLedger: startLedger,
		Pagination:  &source.Pagination{Limit: limit},
	})
	if err != nil {
		metrics.RPCErrors.Inc()
		return 0, fmt.Errorf("getLedgers: %w", err)
	}

	if len(ledgerResult.Ledgers) == 0 {
		return 0, nil
	}

	// Fetch transactions for this ledger range
	const txPageLimit = 200
	txResult, err := p.rpc.GetTransactions(ctx, source.GetTransactionsParams{
		StartLedger: startLedger,
		Pagination:  &source.Pagination{Limit: txPageLimit},
	})
	if err != nil {
		metrics.RPCErrors.Inc()
		return 0, fmt.Errorf("getTransactions: %w", err)
	}

	// Paginate through all transactions in this range
	allTxEntries := txResult.Transactions
	for txResult.Cursor != "" {
		// If the previous page was not full, we've exhausted all available data.
		// This also handles the edge case where the target ledger IS the latest
		// ledger: the RPC returns a partial page with only that ledger's txs and
		// a cursor that loops back, causing infinite duplicate fetches.
		if len(txResult.Transactions) < txPageLimit {
			break
		}
		lastTxLedger := uint32(0)
		if len(txResult.Transactions) > 0 {
			lastTxLedger = txResult.Transactions[len(txResult.Transactions)-1].Ledger
		}
		// Stop paginating when we've passed our ledger range
		endLedger := ledgerResult.Ledgers[len(ledgerResult.Ledgers)-1].Sequence
		if lastTxLedger > endLedger {
			break
		}

		txResult, err = p.rpc.GetTransactions(ctx, source.GetTransactionsParams{
			Pagination: &source.Pagination{Cursor: txResult.Cursor, Limit: txPageLimit},
		})
		if err != nil {
			metrics.RPCErrors.Inc()
			return 0, fmt.Errorf("getTransactions (pagination): %w", err)
		}
		allTxEntries = append(allTxEntries, txResult.Transactions...)
	}

	// Group transactions by ledger
	txByLedger := make(map[uint32][]source.TransactionEntry)
	for _, tx := range allTxEntries {
		txByLedger[tx.Ledger] = append(txByLedger[tx.Ledger], tx)
	}

	// Process each ledger
	processed := 0
	for _, ledgerEntry := range ledgerResult.Ledgers {
		if err := p.processOneLedger(ctx, ledgerEntry, txByLedger[ledgerEntry.Sequence]); err != nil {
			return processed, fmt.Errorf("processLedger %d: %w", ledgerEntry.Sequence, err)
		}
		processed++
	}

	return processed, nil
}

func (p *LivePipeline) processOneLedger(ctx context.Context, ledgerEntry source.LedgerEntry, txEntries []source.TransactionEntry) error {
	return ProcessOneLedger(ctx, p.rpc, p.store, p.publisher, p.networkPassphrase, ledgerEntry, txEntries)
}

// ProcessOneLedger transforms and stores a single ledger with its transactions and operations.
// It is exported so that different pipeline implementations (live, backfill, S3) can reuse it.
// rpc may be nil — when provided, new contracts discovered in the ledger are processed asynchronously.
func ProcessOneLedger(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, pub Publisher, networkPassphrase string, ledgerEntry source.LedgerEntry, txEntries []source.TransactionEntry) error {
	// Transform ledger
	ledger, err := transform.LedgerFromRPC(ledgerEntry)
	if err != nil {
		return fmt.Errorf("transform ledger: %w", err)
	}
	ledger.TransactionCount = int32(len(txEntries))

	// Count successes/failures
	var successCount, failCount int32
	for _, tx := range txEntries {
		if tx.Status == "SUCCESS" {
			successCount++
		} else {
			failCount++
		}
	}
	ledger.SuccessfulTxCount = successCount
	ledger.FailedTxCount = failCount

	// Transform transactions and operations
	var storeTxs []store.Transaction
	var storeOps []store.Operation
	var opCount int32

	for _, txEntry := range txEntries {
		tx, err := transform.TransactionFromRPC(txEntry, networkPassphrase)
		if err != nil {
			log.Printf("ledger %d: skip tx: %v", ledgerEntry.Sequence, err)
			continue
		}
		storeTxs = append(storeTxs, *tx)

		ops, err := transform.OperationsFromRPC(txEntry, networkPassphrase)
		if err != nil {
			log.Printf("ledger %d: skip ops for tx %s: %v", ledgerEntry.Sequence, tx.Hash, err)
			continue
		}
		storeOps = append(storeOps, ops...)
		opCount += int32(len(ops))
	}
	ledger.OperationCount = opCount

	// Extract CAP-67 token events from LedgerCloseMeta
	tokenEvents, err := transform.TokenEventsFromLedgerMeta(ledgerEntry.MetadataXDR, networkPassphrase)
	if err != nil {
		log.Printf("ledger %d: token event extraction warning: %v", ledgerEntry.Sequence, err)
		// Non-fatal: continue without token events
		tokenEvents = nil
	}

	// Extract contract events from each transaction's result meta
	var contractEvents []store.ContractEvent
	for _, txEntry := range txEntries {
		ces, err := transform.ContractEventsFromTransaction(txEntry, networkPassphrase)
		if err != nil {
			log.Printf("ledger %d: contract event extraction warning: %v", ledgerEntry.Sequence, err)
			continue
		}
		contractEvents = append(contractEvents, ces...)
	}

	// Write to database
	if err := db.InsertLedger(ctx, ledger); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("insert ledger: %w", err)
	}
	if err := db.InsertTransactionBatch(ctx, storeTxs); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("insert transactions: %w", err)
	}
	if err := db.InsertOperationBatch(ctx, storeOps); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("insert operations: %w", err)
	}
	// Detect newly created contracts and process their specs asynchronously
	if rpc != nil && ledgerEntry.MetadataXDR != "" {
		closedAt := ledger.ClosedAt
		if detected, err := transform.DetectNewContracts(ledgerEntry.MetadataXDR, ledgerEntry.Sequence, closedAt); err != nil {
			log.Printf("ledger %d: detect contracts warning: %v", ledgerEntry.Sequence, err)
		} else {
			log.Printf("ledger %d: detected %d new contracts", ledgerEntry.Sequence, len(detected))
			for _, c := range detected {
				go transform.ProcessContractSpec(context.Background(), rpc, db, c)
			}
		}
	} else {
		log.Printf("ledger %d: skipping contract detection (rpc=%v metaXDR_empty=%v)", ledgerEntry.Sequence, rpc == nil, ledgerEntry.MetadataXDR == "")
	}

	if err := db.InsertTokenEventBatch(ctx, tokenEvents); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("insert token events: %w", err)
	}
	if err := db.InsertContractEventBatch(ctx, contractEvents); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("insert contract events: %w", err)
	}

	// Update cursor
	if err := db.SetLastIngestedLedger(ctx, ledgerEntry.Sequence); err != nil {
		metrics.DBErrors.Inc()
		return fmt.Errorf("update cursor: %w", err)
	}

	metrics.LedgersIngested.Inc()
	metrics.TransactionsIngested.Add(float64(len(storeTxs)))
	metrics.OperationsIngested.Add(float64(len(storeOps)))

	// Publish if publisher is set
	if pub != nil {
		_ = pub.PublishLedger(ctx, ledger)
		_ = pub.PublishTransactions(ctx, storeTxs)
	}

	log.Printf("ingested ledger %d (%d txs, %d ops)",
		ledgerEntry.Sequence, len(storeTxs), len(storeOps))

	return nil
}

// maxGapFillPerTick bounds how many missing ledgers a single gap-detection
// pass will attempt to backfill, so a large or old gap can't turn one tick
// into an unbounded historical replay.
const maxGapFillPerTick = 500

// ledgerRange is an inclusive [start, end] sequence range.
type ledgerRange struct {
	start uint32
	end   uint32
}

// contiguousRanges collapses a sorted, deduplicated list of missing ledger
// sequences into the minimal set of contiguous [start, end] ranges, so each
// run can be re-fetched from RPC in one batch instead of one call per ledger.
func contiguousRanges(seqs []uint32) []ledgerRange {
	if len(seqs) == 0 {
		return nil
	}

	ranges := []ledgerRange{{start: seqs[0], end: seqs[0]}}
	for _, seq := range seqs[1:] {
		last := &ranges[len(ranges)-1]
		if seq == last.end+1 {
			last.end = seq
			continue
		}
		ranges = append(ranges, ledgerRange{start: seq, end: seq})
	}
	return ranges
}

// detectAndFillGaps looks for ledger sequences between the oldest and latest
// ingested ledger that have no row in the ledgers table (e.g. left behind by
// a network blip, restart, or RPC outage) and re-ingests them via RPC,
// reusing the same processing path as normal live ingestion.
func (p *LivePipeline) detectAndFillGaps(ctx context.Context) {
	log.Println("live pipeline: running gap detection")

	minSeq, maxSeq, err := p.store.GetLedgerSequenceBounds(ctx)
	if err != nil {
		log.Printf("live pipeline: gap detection: failed to get ledger bounds: %v", err)
		return
	}
	if maxSeq == 0 || maxSeq <= minSeq {
		log.Println("live pipeline: gap detection: no gaps possible (not enough ledgers ingested yet)")
		return
	}

	missing, err := p.store.FindMissingLedgerSequences(ctx, minSeq, maxSeq, maxGapFillPerTick)
	if err != nil {
		log.Printf("live pipeline: gap detection: failed to query missing sequences: %v", err)
		return
	}
	if len(missing) == 0 {
		log.Println("live pipeline: gap detection: no gaps found")
		return
	}

	log.Printf("live pipeline: gap detection: found %d missing ledger(s) between %d and %d", len(missing), minSeq, maxSeq)

	for _, r := range contiguousRanges(missing) {
		filled, err := p.fillGapRange(ctx, r)
		if err != nil {
			log.Printf("live pipeline: gap fill failed for range %d-%d: %v", r.start, r.end, err)
			continue
		}
		log.Printf("live pipeline: gap fill: refilled %d/%d ledger(s) in range %d-%d", filled, r.end-r.start+1, r.start, r.end)
	}
}

// fillGapRange re-fetches and re-ingests every ledger in [r.start, r.end],
// chunking the work by the pipeline's normal batch size.
func (p *LivePipeline) fillGapRange(ctx context.Context, r ledgerRange) (int, error) {
	filled := 0
	cursor := r.start
	for cursor <= r.end {
		select {
		case <-ctx.Done():
			return filled, ctx.Err()
		default:
		}

		remaining := int(r.end - cursor + 1)
		limit := p.batchSize
		if remaining < limit {
			limit = remaining
		}

		count, err := p.processLedgerBatch(ctx, cursor, limit)
		if err != nil {
			return filled, fmt.Errorf("processLedgerBatch at %d: %w", cursor, err)
		}
		if count == 0 {
			// RPC no longer has this ledger available (e.g. beyond its
			// retention window) — nothing more we can do for this range.
			break
		}

		cursor += uint32(count)
		filled += count
	}
	return filled, nil
}
