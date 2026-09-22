package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"logd/internal/queue"
)

func (db *DB) InsertEvents(ctx context.Context, events []queue.Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin event batch: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	batch := &pgx.Batch{}
	for _, event := range events {
		batch.Queue(`
			INSERT INTO log_events (
				event_id,
				event_time,
				level,
				project_key,
				node_key,
				request_ip,
				member,
				session_id,
				request_method,
				request_url,
				request_headers,
				request_params,
				error_scene,
				error_message,
				error_file,
				error_line,
				error_stack
			)
			VALUES (
				$1::uuid,
				$2::timestamptz,
				$3,
				$4,
				$5,
				$6::inet,
				$7,
				$8,
				$9,
				$10,
				$11::jsonb,
				$12::jsonb,
				$13,
				$14,
				$15,
				$16,
				$17
			)
			ON CONFLICT (level, event_time, event_id) DO NOTHING
		`,
			event.EventID,
			event.EventTime,
			event.Level,
			event.ProjectKey,
			event.NodeKey,
			event.RequestIP,
			event.Member,
			event.SessionID,
			event.RequestMethod,
			event.RequestURL,
			jsonValue(event.RequestHeaders),
			jsonValue(event.RequestParams),
			event.ErrorScene,
			event.ErrorMessage,
			optionalString(event.ErrorFile),
			optionalInt(event.ErrorLine),
			optionalString(event.ErrorStack),
		)
	}

	results := tx.SendBatch(ctx, batch)
	for range events {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("insert event batch: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("close event batch: %w", err)
	}
	if err := upsertLogDimensions(ctx, tx, events); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit event batch: %w", err)
	}
	return nil
}

func upsertLogDimensions(ctx context.Context, tx pgx.Tx, events []queue.Event) error {
	type dimensionKey struct {
		projectKey string
		nodeKey    string
		level      string
	}

	dimensions := make(map[dimensionKey]time.Time)
	for _, event := range events {
		key := dimensionKey{
			projectKey: event.ProjectKey,
			nodeKey:    event.NodeKey,
			level:      event.Level,
		}
		if lastSeenAt, ok := dimensions[key]; !ok || event.EventTime.After(lastSeenAt) {
			dimensions[key] = event.EventTime
		}
	}

	projectKeys := make([]string, 0, len(dimensions))
	nodeKeys := make([]string, 0, len(dimensions))
	levels := make([]string, 0, len(dimensions))
	lastSeenAt := make([]time.Time, 0, len(dimensions))
	for key, seenAt := range dimensions {
		projectKeys = append(projectKeys, key.projectKey)
		nodeKeys = append(nodeKeys, key.nodeKey)
		levels = append(levels, key.level)
		lastSeenAt = append(lastSeenAt, seenAt)
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO log_dimensions (
			project_key,
			node_key,
			level,
			last_seen_at
		)
		SELECT project_key, node_key, level, last_seen_at
		FROM unnest(
			$1::text[],
			$2::text[],
			$3::text[],
			$4::timestamptz[]
		) AS dimensions(project_key, node_key, level, last_seen_at)
		ON CONFLICT (project_key, node_key, level)
		DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
		WHERE log_dimensions.last_seen_at < EXCLUDED.last_seen_at
	`, projectKeys, nodeKeys, levels, lastSeenAt)
	if err != nil {
		return fmt.Errorf("upsert log dimensions: %w", err)
	}
	return nil
}

func jsonValue(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
