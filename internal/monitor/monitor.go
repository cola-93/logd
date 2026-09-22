package monitor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"logd/internal/queue"
	"logd/internal/store"
)

const (
	evaluationInterval = 30 * time.Second
	errorRateWindow    = 5 * time.Minute
)

var durationBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type Metrics struct {
	queue          *queue.Queue
	lastWrite      func() time.Time
	ingestWindow   [30]ingestBucket
	mu             sync.Mutex
	ingestRequests map[int]uint64
	ingestEvents   map[string]uint64
	queueRejected  uint64
	writeBatches   map[string]uint64
	writeEvents    map[string]uint64
	writeDuration  [9]uint64
	writeSeconds   float64
	writeCount     uint64
}

type ingestBucket struct {
	epoch  int64
	total  uint64
	errors uint64
}

type Alert struct {
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

type Manager struct {
	db        *store.DB
	queue     *queue.Queue
	metrics   *Metrics
	lastWrite func() time.Time
	logger    *slog.Logger
	mu        sync.Mutex
	since     map[string]time.Time
	active    map[string]Alert
}

func NewMetrics(events *queue.Queue) *Metrics {
	return &Metrics{
		queue:          events,
		ingestRequests: make(map[int]uint64),
		ingestEvents:   make(map[string]uint64),
		writeBatches:   make(map[string]uint64),
		writeEvents:    make(map[string]uint64),
	}
}

func (m *Metrics) SetLastWriteProvider(provider func() time.Time) {
	m.lastWrite = provider
}

func (m *Metrics) RecordIngest(status int) {
	now := time.Now()
	epoch := now.Unix() / 10

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ingestRequests[status]++

	index := int(epoch % int64(len(m.ingestWindow)))
	bucket := &m.ingestWindow[index]
	if bucket.epoch != epoch {
		bucket.epoch = epoch
		bucket.total = 0
		bucket.errors = 0
	}
	bucket.total++
	if status >= http.StatusBadRequest {
		bucket.errors++
	}
}

func (m *Metrics) RecordIngestEvents(events []queue.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, event := range events {
		m.ingestEvents[event.Level]++
	}
}

func (m *Metrics) RecordQueueRejected() {
	m.mu.Lock()
	m.queueRejected++
	m.mu.Unlock()
}

func (m *Metrics) RecordDBWrite(result string, events int, duration time.Duration) {
	seconds := duration.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeBatches[result]++
	m.writeEvents[result] += uint64(events)
	m.writeSeconds += seconds
	m.writeCount++
	for index, upper := range durationBuckets {
		if seconds <= upper {
			m.writeDuration[index]++
			break
		}
	}
}

func (m *Metrics) WindowStats(window time.Duration) (uint64, uint64) {
	cutoff := time.Now().Add(-window).Unix() / 10

	m.mu.Lock()
	defer m.mu.Unlock()
	var total uint64
	var errors uint64
	for _, bucket := range m.ingestWindow {
		if bucket.epoch < cutoff {
			continue
		}
		total += bucket.total
		errors += bucket.errors
	}
	return total, errors
}

func (m *Metrics) WritePrometheus(w io.Writer) {
	m.mu.Lock()
	ingestRequests := cloneMap(m.ingestRequests)
	ingestEvents := cloneMap(m.ingestEvents)
	queueRejected := m.queueRejected
	writeBatches := cloneMap(m.writeBatches)
	writeEvents := cloneMap(m.writeEvents)
	writeDuration := m.writeDuration
	writeSeconds := m.writeSeconds
	writeCount := m.writeCount
	m.mu.Unlock()

	fmt.Fprintln(w, "# HELP logd_ingest_requests_total Ingest HTTP requests by status.")
	fmt.Fprintln(w, "# TYPE logd_ingest_requests_total counter")
	for _, status := range sortedIntKeys(ingestRequests) {
		fmt.Fprintf(w, "logd_ingest_requests_total{status=%q} %d\n", strconv.Itoa(status), ingestRequests[status])
	}

	fmt.Fprintln(w, "# HELP logd_ingest_events_total Accepted ingest events by level.")
	fmt.Fprintln(w, "# TYPE logd_ingest_events_total counter")
	for _, level := range []string{"INFO", "WARN", "ERROR", "DEBUG"} {
		fmt.Fprintf(w, "logd_ingest_events_total{level=%q} %d\n", level, ingestEvents[level])
	}

	fmt.Fprintln(w, "# HELP logd_queue_depth Current ingest queue depth.")
	fmt.Fprintln(w, "# TYPE logd_queue_depth gauge")
	fmt.Fprintf(w, "logd_queue_depth %d\n", m.queue.Depth())

	fmt.Fprintln(w, "# HELP logd_queue_capacity Ingest queue capacity.")
	fmt.Fprintln(w, "# TYPE logd_queue_capacity gauge")
	fmt.Fprintf(w, "logd_queue_capacity %d\n", m.queue.Capacity())

	fmt.Fprintln(w, "# HELP logd_queue_rejected_total Batches rejected because the queue is full.")
	fmt.Fprintln(w, "# TYPE logd_queue_rejected_total counter")
	fmt.Fprintf(w, "logd_queue_rejected_total %d\n", queueRejected)

	fmt.Fprintln(w, "# HELP logd_db_write_batches_total PostgreSQL write batches by result.")
	fmt.Fprintln(w, "# TYPE logd_db_write_batches_total counter")
	for _, result := range []string{"success", "error"} {
		fmt.Fprintf(w, "logd_db_write_batches_total{result=%q} %d\n", result, writeBatches[result])
	}

	fmt.Fprintln(w, "# HELP logd_db_write_events_total PostgreSQL events attempted by result.")
	fmt.Fprintln(w, "# TYPE logd_db_write_events_total counter")
	for _, result := range []string{"success", "error"} {
		fmt.Fprintf(w, "logd_db_write_events_total{result=%q} %d\n", result, writeEvents[result])
	}

	fmt.Fprintln(w, "# HELP logd_db_write_duration_seconds PostgreSQL write attempt duration.")
	fmt.Fprintln(w, "# TYPE logd_db_write_duration_seconds histogram")
	var cumulative uint64
	for index, upper := range durationBuckets {
		cumulative += writeDuration[index]
		fmt.Fprintf(
			w,
			"logd_db_write_duration_seconds_bucket{le=%q} %d\n",
			strconv.FormatFloat(upper, 'f', -1, 64),
			cumulative,
		)
	}
	fmt.Fprintf(w, "logd_db_write_duration_seconds_bucket{le=\"+Inf\"} %d\n", writeCount)
	fmt.Fprintf(w, "logd_db_write_duration_seconds_sum %g\n", writeSeconds)
	fmt.Fprintf(w, "logd_db_write_duration_seconds_count %d\n", writeCount)

	lastWriteSeconds := float64(0)
	if m.lastWrite != nil {
		if value := m.lastWrite(); !value.IsZero() {
			lastWriteSeconds = float64(value.UnixMilli()) / 1000
		}
	}
	fmt.Fprintln(w, "# HELP logd_last_successful_db_write_timestamp_seconds Unix timestamp of the last successful write.")
	fmt.Fprintln(w, "# TYPE logd_last_successful_db_write_timestamp_seconds gauge")
	fmt.Fprintf(w, "logd_last_successful_db_write_timestamp_seconds %g\n", lastWriteSeconds)
}

func NewManager(
	db *store.DB,
	events *queue.Queue,
	metrics *Metrics,
	lastWrite func() time.Time,
	logger *slog.Logger,
) *Manager {
	return &Manager{
		db:        db,
		queue:     events,
		metrics:   metrics,
		lastWrite: lastWrite,
		logger:    logger,
		since:     make(map[string]time.Time),
		active:    make(map[string]Alert),
	}
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(evaluationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.evaluate(ctx)
		}
	}
}

func (m *Manager) ActiveAlerts() []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()

	alerts := make([]Alert, 0, len(m.active))
	for _, alert := range m.active {
		alerts = append(alerts, alert)
	}
	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].Key < alerts[j].Key
	})
	return alerts
}

func (m *Manager) Metrics() *Metrics {
	return m.metrics
}

func (m *Manager) evaluate(ctx context.Context) {
	now := time.Now()
	_, settingsErr := m.db.SystemSettings(ctx)

	databaseOK := settingsErr == nil && m.db.Ping(ctx) == nil
	m.setCondition("database_unavailable", !databaseOK, "PostgreSQL 不可用", 2*time.Minute, now)

	depth := m.queue.Depth()
	capacity := m.queue.Capacity()
	queueHigh := capacity > 0 && float64(depth)/float64(capacity) > 0.8
	m.setCondition("queue_high", queueHigh, "接收队列使用率超过 80%", 5*time.Minute, now)

	lastWrite := m.lastWrite()
	writeStalled := depth > 0 && (lastWrite.IsZero() || now.Sub(lastWrite) >= 2*time.Minute)
	m.setCondition("write_stalled", writeStalled, "队列非空且连续 2 分钟没有成功写入", 2*time.Minute, now)

	total, errors := m.metrics.WindowStats(errorRateWindow)
	errorRateHigh := total > 0 && float64(errors)/float64(total) > 0.05
	m.setCondition("ingest_error_rate", errorRateHigh, "最近 5 分钟接收错误率超过 5%", 0, now)
}

func (m *Manager) setCondition(
	key string,
	firing bool,
	message string,
	duration time.Duration,
	now time.Time,
) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !firing {
		delete(m.since, key)
		delete(m.active, key)
		return
	}

	started, exists := m.since[key]
	if !exists {
		m.since[key] = now
		started = now
	}
	if now.Sub(started) < duration {
		return
	}

	alert := Alert{Key: key, Message: message, Since: started}
	_, wasActive := m.active[key]
	m.active[key] = alert
	if wasActive {
		return
	}
	m.logger.Warn("alert firing", "alert", key, "message", message, "since", started)
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func sortedIntKeys(values map[int]uint64) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}
