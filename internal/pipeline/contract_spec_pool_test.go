package pipeline

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/source"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
	"github.com/miguelnietoa/stellar-explorer/indexer/internal/transform"
)

// TestContractSpecPoolCapsConcurrency verifies that at most workerCount jobs
// run ProcessContractSpec at once, even when many more are submitted.
func TestContractSpecPoolCapsConcurrency(t *testing.T) {
	const workers = 3
	const jobs = 20

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	gate := make(chan struct{})

	pool := newContractSpecPool(nil, nil, workers)
	pool.process = func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract) {
		n := concurrent.Add(1)
		for {
			cur := maxConcurrent.Load()
			if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
				break
			}
		}
		<-gate
		concurrent.Add(-1)
	}
	ctx := context.Background()
	pool.Start(ctx)

	var submitWG sync.WaitGroup
	for i := 0; i < jobs; i++ {
		submitWG.Add(1)
		go func() {
			defer submitWG.Done()
			if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "c"}); err != nil {
				t.Errorf("Submit: %v", err)
			}
		}()
	}

	deadline := time.After(2 * time.Second)
	for concurrent.Load() < workers {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d concurrent workers, got %d", workers, concurrent.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if got := maxConcurrent.Load(); got > workers {
		t.Fatalf("max concurrent = %d, want <= %d", got, workers)
	}
	if got := concurrent.Load(); got != workers {
		t.Fatalf("concurrent = %d, want %d", got, workers)
	}

	close(gate)
	submitWG.Wait()
	pool.Stop()

	if got := maxConcurrent.Load(); got > workers {
		t.Fatalf("after drain: max concurrent = %d, want <= %d", got, workers)
	}
}

// TestContractSpecPoolStopWaitsForInflight verifies Stop does not return until
// the in-flight ProcessContractSpec call finishes (no leaked worker goroutines).
func TestContractSpecPoolStopWaitsForInflight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool

	pool := newContractSpecPool(nil, nil, 1)
	pool.process = func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract) {
		close(started)
		<-release
		finished.Store(true)
	}
	ctx := context.Background()
	pool.Start(ctx)
	if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "inflight"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("job never started")
	}

	stopDone := make(chan struct{})
	go func() {
		pool.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight work finished")
	case <-time.After(50 * time.Millisecond):
	}

	if finished.Load() {
		t.Fatal("in-flight work finished before release")
	}

	close(release)

	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after in-flight work finished")
	}

	if !finished.Load() {
		t.Fatal("expected in-flight work to have finished")
	}
}

// TestContractSpecPoolStopDrainsQueued verifies Stop drains jobs still in the
// queue (not only the currently executing one).
func TestContractSpecPoolStopDrainsQueued(t *testing.T) {
	const workers = 1
	const queued = 5

	var processed atomic.Int32
	startedFirst := make(chan struct{})
	release := make(chan struct{})

	pool := newContractSpecPool(nil, nil, workers)
	pool.process = func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract) {
		if processed.Add(1) == 1 {
			close(startedFirst)
			<-release
		}
	}
	ctx := context.Background()
	pool.Start(ctx)

	var submitWG sync.WaitGroup
	for i := 0; i < queued; i++ {
		submitWG.Add(1)
		go func() {
			defer submitWG.Done()
			if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "q"}); err != nil {
				t.Errorf("Submit: %v", err)
			}
		}()
	}

	select {
	case <-startedFirst:
	case <-time.After(2 * time.Second):
		t.Fatal("first job never started")
	}

	// Let Submit goroutines fill the buffer while the worker is blocked.
	time.Sleep(20 * time.Millisecond)

	stopDone := make(chan struct{})
	go func() {
		close(release)
		submitWG.Wait()
		pool.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not finish draining queued jobs")
	}

	if got := processed.Load(); got != queued {
		t.Fatalf("processed = %d, want %d", got, queued)
	}
}

// TestContractSpecPoolSubmitRespectsCancel verifies Submit returns when the
// pipeline context is cancelled while the queue is full, avoiding shutdown deadlock.
func TestContractSpecPoolSubmitRespectsCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	pool := newContractSpecPool(nil, nil, 1)
	pool.process = func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract) {
		once.Do(func() { close(started) })
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	// Fill worker + buffer (workers*2 = 2) so the next Submit blocks.
	if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "1"}); err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first job never started")
	}
	if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "2"}); err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "3"}); err != nil {
		t.Fatalf("Submit 3: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pool.Submit(ctx, transform.DetectedContract{ContractID: "blocked"})
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Submit to return ctx error when cancelled on full queue")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit did not return after context cancel")
	}

	close(release)
	pool.Stop()
}

// TestContractSpecPoolNoGoroutineLeakAfterStop is a lightweight leak check:
// after Stop, goroutine count should not keep growing across repeated pool cycles.
// Run with -race for data-race coverage of the pool itself.
func TestContractSpecPoolNoGoroutineLeakAfterStop(t *testing.T) {
	runtime.GC()
	base := runtime.NumGoroutine()

	for round := 0; round < 5; round++ {
		var processed atomic.Int32
		pool := newContractSpecPool(nil, nil, 4)
		pool.process = func(ctx context.Context, rpc *source.RPCClient, db *store.PostgresStore, c transform.DetectedContract) {
			processed.Add(1)
		}
		ctx := context.Background()
		pool.Start(ctx)
		for i := 0; i < 16; i++ {
			if err := pool.Submit(ctx, transform.DetectedContract{ContractID: "x"}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
		}
		pool.Stop()
		if got := processed.Load(); got != 16 {
			t.Fatalf("round %d: processed = %d, want 16", round, got)
		}
	}

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	// Allow some slack for the testing runtime; a real leak would grow unboundedly.
	if after > base+10 {
		t.Fatalf("goroutine count grew suspiciously: before=%d after=%d", base, after)
	}
}
