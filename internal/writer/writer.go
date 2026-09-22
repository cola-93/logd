package writer

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"logd/internal/config"
	"logd/internal/monitor"
	"logd/internal/queue"
	"logd/internal/store"
)

type Writer struct {
	queue       *queue.Queue
	store       *store.DB
	config      config.QueueConfig
	logger      *slog.Logger
	metrics     *monitor.Metrics
	done        chan struct{}
	lastSuccess atomic.Int64
}

func New(
	events *queue.Queue,
	db *store.DB,
	cfg config.QueueConfig,
	logger *slog.Logger,
	metrics *monitor.Metrics,
) *Writer {
	return &Writer{
		queue:   events,
		store:   db,
		config:  cfg,
		logger:  logger,
		metrics: metrics,
		done:    make(chan struct{}),
	}
}

func (w *Writer) Run(ctx context.Context) {
	defer close(w.done)

	for {
		batch, ok := w.queue.ReadBatch(
			ctx,
			w.config.WriteBatchSize,
			w.config.FlushInterval(),
		)
		if !ok {
			return
		}
		if !w.write(ctx, batch) {
			return
		}
	}
}

func (w *Writer) Done() <-chan struct{} {
	return w.done
}

func (w *Writer) LastSuccessfulWrite() time.Time {
	milliseconds := w.lastSuccess.Load()
	if milliseconds == 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds)
}

func (w *Writer) write(ctx context.Context, batch []queue.Event) bool {
	delay := w.config.RetryInitial()
	for {
		started := time.Now()
		err := w.store.InsertEvents(ctx, batch)
		duration := time.Since(started)
		if err == nil {
			w.lastSuccess.Store(time.Now().UnixMilli())
			if w.metrics != nil {
				w.metrics.RecordDBWrite("success", len(batch), duration)
			}
			w.logger.Debug("event batch written", "events", len(batch))
			return true
		} else {
			if w.metrics != nil {
				w.metrics.RecordDBWrite("error", len(batch), duration)
			}
			w.logger.Error(
				"event batch write failed",
				"events", len(batch),
				"retry_in", delay,
				"error", err,
			)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false
		case <-timer.C:
		}

		delay *= 2
		if delay > w.config.RetryMax() {
			delay = w.config.RetryMax()
		}
	}
}
