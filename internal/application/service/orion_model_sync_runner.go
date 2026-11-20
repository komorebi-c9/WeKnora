package service

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	wkruntime "github.com/Tencent/WeKnora/internal/runtime"
)

const (
	// orionSyncStartupDelay spreads the first sweep across replicas. The sync
	// itself is idempotent, but N replicas hitting Orion at the same instant is
	// still pointless load.
	orionSyncStartupDelay = 5 * time.Second
	// orionSyncRunTimeout bounds one full sweep (all workspaces).
	orionSyncRunTimeout = 10 * time.Minute
)

// OrionModelSyncRunner refreshes the Orion catalogue on a fixed cadence.
//
// It starts only when the switch is on, mirrors the AuditLogRetentionRunner
// lifecycle (StopWithin + ResourceCleaner), and never uses the request context
// from Start: that context is cancelled as soon as container init returns.
type OrionModelSyncRunner struct {
	sync     *OrionModelSync
	interval time.Duration

	stopCh   chan struct{}
	doneCh   chan struct{}
	started  atomic.Bool
	stopOnce sync.Once
}

// NewOrionModelSyncRunner builds the runner. A disabled synchroniser yields a
// runner whose Start is a no-op.
func NewOrionModelSyncRunner(sync *OrionModelSync) *OrionModelSyncRunner {
	interval := 5 * time.Minute
	if sync != nil && sync.cfg != nil {
		interval = sync.cfg.SyncInterval()
	}
	return &OrionModelSyncRunner{
		sync:     sync,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start launches the loop once. It returns immediately.
func (r *OrionModelSyncRunner) Start(_ context.Context) {
	if r == nil || r.sync == nil || !r.sync.Enabled() {
		return
	}
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	logger.Infof(context.Background(),
		"[orion-model] sync runner started: interval=%s", r.interval)
	go r.loop()
}

// StopWithin stops the loop and waits up to timeout for the in-flight sweep.
// A non-positive timeout waits indefinitely. Idempotent.
func (r *OrionModelSyncRunner) StopWithin(timeout time.Duration) {
	if r == nil || !r.started.Load() {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	if wkruntime.WaitFor(r.doneCh, timeout) {
		return
	}
	logger.Warnf(context.Background(),
		"[orion-model] sync still running after %s; continuing shutdown", timeout)
}

func (r *OrionModelSyncRunner) loop() {
	defer close(r.doneCh)

	startup := time.NewTimer(orionSyncStartupDelay)
	defer startup.Stop()
	select {
	case <-startup.C:
	case <-r.stopCh:
		return
	}
	r.runOnce()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.jitter()
			select {
			case <-r.stopCh:
				return
			default:
			}
			r.runOnce()
		case <-r.stopCh:
			return
		}
	}
}

// jitter sleeps up to 10% of the interval so replicas drift apart. It is
// interruptible so shutdown is not delayed by a sleeping loop.
func (r *OrionModelSyncRunner) jitter() {
	window := r.interval / 10
	if window <= 0 {
		return
	}
	delay := time.Duration(rand.Int63n(int64(window)))
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.stopCh:
	}
}

func (r *OrionModelSyncRunner) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), orionSyncRunTimeout)
	defer cancel()
	r.sync.SyncAll(ctx)
}
