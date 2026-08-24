package pipeline

import (
	"context"
	"sync"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/transform"
)

// contractSpecPool runs ProcessContractSpec with a fixed number of workers.
// Submit blocks when all workers are busy and the queue is full, providing
// backpressure so a burst of new contracts cannot spawn unbounded goroutines
// or hammer the RPC endpoint.
type contractSpecPool struct {
	rpc     *source.RPCClient
	store   *store.PostgresStore
	jobs    chan transform.DetectedContract
	workers int
	wg      sync.WaitGroup

	// process defaults to transform.ProcessContractSpec; overridden in tests.
	process func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract)
}

func newContractSpecPool(rpc *source.RPCClient, db *store.PostgresStore, workers int) *contractSpecPool {
	if workers <= 0 {
		workers = 1
	}
	return &contractSpecPool{
		rpc:     rpc,
		store:   db,
		jobs:    make(chan transform.DetectedContract, workers*2),
		workers: workers,
		process: transform.ProcessContractSpec,
	}
}

// Start launches the worker goroutines. Jobs are processed with ctx so RPC
// calls abort promptly when the pipeline is cancelled; Stop still waits for
// workers to finish in-flight (and queued) work before returning.
func (p *contractSpecPool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.loop(ctx)
	}
}

func (p *contractSpecPool) loop(ctx context.Context) {
	defer p.wg.Done()
	for job := range p.jobs {
		p.process(ctx, p.rpc, p.store, job)
	}
}

// Submit enqueues a detected contract for spec processing. It blocks if the
// queue is full until a worker accepts the job, or returns ctx.Err() if the
// pipeline context is cancelled first (so shutdown cannot deadlock on a full queue).
// Callers must stop submitting before Stop.
func (p *contractSpecPool) Submit(ctx context.Context, c transform.DetectedContract) error {
	select {
	case p.jobs <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop closes the job queue and waits for workers to drain remaining jobs
// and finish in-flight ProcessContractSpec calls. No goroutines are left
// running after Stop returns.
func (p *contractSpecPool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}
