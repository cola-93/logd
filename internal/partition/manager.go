package partition

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Manager struct {
	pool   *pgxpool.Pool
	loc    *time.Location
	logger *slog.Logger
	mu     sync.Mutex
	ready  atomic.Bool
}

func New(pool *pgxpool.Pool, loc *time.Location, logger *slog.Logger) *Manager {
	return &Manager{
		pool:   pool,
		loc:    loc,
		logger: logger,
	}
}

func (m *Manager) Ready() bool {
	return m.ready.Load()
}

func (m *Manager) Ensure(ctx context.Context, retentionDays int) error {
	if retentionDays < 1 {
		return fmt.Errorf("retention days must be at least 1")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().In(m.loc)
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, m.loc)
	for offset := -1; offset <= 1; offset++ {
		if err := m.ensureErrorMonth(ctx, currentMonth.AddDate(0, offset, 0)); err != nil {
			return err
		}
	}

	cutoff := now.AddDate(0, 0, -retentionDays)
	firstDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, m.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.loc)
	lastDay := today.AddDate(0, 0, 1)

	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		if err := m.ensureOtherDay(ctx, day); err != nil {
			return err
		}
	}
	if err := m.cleanupOtherDays(ctx, cutoff); err != nil {
		return err
	}

	m.ready.Store(true)
	m.logger.Info(
		"partitions ready",
		"timezone", m.loc.String(),
		"retention_days", retentionDays,
		"window_start", firstDay.Format(time.RFC3339),
		"window_end", lastDay.AddDate(0, 0, 1).Format(time.RFC3339),
	)
	return nil
}

func (m *Manager) ensureErrorMonth(ctx context.Context, month time.Time) error {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, m.loc)
	end := start.AddDate(0, 1, 0)
	name := "log_events_error_" + start.Format("200601")
	sql := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF log_events_error FOR VALUES FROM (%s) TO (%s)",
		pgx.Identifier{name}.Sanitize(),
		quoteTimestamp(start),
		quoteTimestamp(end),
	)
	if _, err := m.pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("ensure error partition %s: %w", name, err)
	}
	return nil
}

func (m *Manager) ensureOtherDay(ctx context.Context, day time.Time) error {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, m.loc)
	end := start.AddDate(0, 0, 1)
	name := "log_events_other_" + start.Format("20060102")
	sql := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF log_events_other FOR VALUES FROM (%s) TO (%s)",
		pgx.Identifier{name}.Sanitize(),
		quoteTimestamp(start),
		quoteTimestamp(end),
	)
	if _, err := m.pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("ensure other partition %s: %w", name, err)
	}
	return nil
}

func quoteTimestamp(value time.Time) string {
	return "'" + value.Format(time.RFC3339Nano) + "'"
}

func (m *Manager) cleanupOtherDays(ctx context.Context, cutoff time.Time) error {
	rows, err := m.pool.Query(ctx, `
		SELECT child.relname
		FROM pg_inherits
		JOIN pg_class parent ON parent.oid = inhparent
		JOIN pg_class child ON child.oid = inhrelid
		WHERE parent.relname = 'log_events_other'
		  AND child.relname ~ '^log_events_other_[0-9]{8}$'
	`)
	if err != nil {
		return fmt.Errorf("list other partitions: %w", err)
	}

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan other partition: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate other partitions: %w", err)
	}
	rows.Close()

	for _, name := range names {
		suffix := strings.TrimPrefix(name, "log_events_other_")
		day, err := time.ParseInLocation("20060102", suffix, m.loc)
		if err != nil {
			m.logger.Warn("skip partition with invalid name", "partition", name, "error", err)
			continue
		}
		if day.AddDate(0, 0, 1).After(cutoff) {
			continue
		}

		sql := "DROP TABLE IF EXISTS " + pgx.Identifier{name}.Sanitize()
		if _, err := m.pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("drop expired partition %s: %w", name, err)
		}
		m.logger.Info("dropped expired partition", "partition", name)
	}
	return nil
}
