package poller

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/relayra/relayra/internal/config"
	"github.com/relayra/relayra/internal/logger"
	"github.com/relayra/relayra/internal/models"
	"github.com/relayra/relayra/internal/relayexec"
	"github.com/relayra/relayra/internal/store"
)

type dispatcher struct {
	cfg      *config.Config
	rdb      store.Backend
	asyncSem chan struct{}
	serialCh chan models.RelayRequest
	wg       sync.WaitGroup
	inFlight atomic.Int64
	leaseTTL time.Duration
}

func newDispatcher(cfg *config.Config, rdb store.Backend) *dispatcher {
	d := &dispatcher{
		cfg:      cfg,
		rdb:      rdb,
		asyncSem: make(chan struct{}, cfg.AsyncWorkers),
		serialCh: make(chan models.RelayRequest, cfg.PollBatchSize*2),
		leaseTTL: senderRequestLeaseDuration(cfg),
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for req := range d.serialCh {
			ctx := logger.WithComponent(context.Background(), "poller")
			ctx = logger.WithRequestID(ctx, req.ID)
			d.execute(ctx, &req)
		}
	}()

	return d
}

func (d *dispatcher) Dispatch(_ context.Context, req models.RelayRequest) {
	d.inFlight.Add(1)

	if req.Async {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.asyncSem <- struct{}{}
			defer func() { <-d.asyncSem }()

			asyncCtx := logger.WithComponent(context.Background(), "poller")
			asyncCtx = logger.WithRequestID(asyncCtx, req.ID)
			d.execute(asyncCtx, &req)
		}()
		return
	}

	d.serialCh <- req
}

func (d *dispatcher) execute(ctx context.Context, req *models.RelayRequest) {
	defer d.inFlight.Add(-1)
	now := time.Now()

	if err := d.rdb.StoreSenderRequestState(ctx, &models.RequestSyncState{
		RequestID:  req.ID,
		Status:     models.StatusExecuting,
		LeaseUntil: now.Add(d.leaseTTL),
		UpdatedAt:  now,
	}); err != nil {
		slog.ErrorContext(ctx, "failed to mark request executing", "error", err)
	}

	slog.InfoContext(ctx, "dispatching request for execution",
		"url", req.Request.URL,
		"method", req.Request.Method,
		"async", req.Async,
	)

	result := relayexec.ExecuteRequest(ctx, req, d.cfg.RequestTimeout)
	if err := d.rdb.PushResult(ctx, result); err != nil {
		slog.ErrorContext(ctx, "failed to store result locally", "error", err)
		return
	}

	doneAt := time.Now()
	if err := d.rdb.StoreSenderRequestState(ctx, &models.RequestSyncState{
		RequestID:  req.ID,
		Status:     models.StatusCompleted,
		LeaseUntil: doneAt.Add(d.leaseTTL),
		UpdatedAt:  doneAt,
	}); err != nil {
		slog.ErrorContext(ctx, "failed to mark request completed", "error", err)
	}
}

func (d *dispatcher) InFlight() int64 {
	return d.inFlight.Load()
}

func (d *dispatcher) Close() {
	close(d.serialCh)
	d.wg.Wait()
}
