package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LogRef struct {
	EventID   string    `json:"event_id"`
	Level     string    `json:"level"`
	EventTime time.Time `json:"event_time"`
}

type DeleteResult struct {
	Deleted       int64 `json:"deleted"`
	SkippedLocked int64 `json:"skipped_locked"`
}

func (db *DB) DeleteLogs(ctx context.Context, items []LogRef) (DeleteResult, error) {
	var result DeleteResult
	if len(items) == 0 {
		return result, nil
	}

	eventIDs := make([]string, 0, len(items))
	levels := make([]string, 0, len(items))
	eventTimes := make([]time.Time, 0, len(items))
	for _, item := range items {
		eventIDs = append(eventIDs, item.EventID)
		levels = append(levels, item.Level)
		eventTimes = append(eventTimes, item.EventTime)
	}

	err := db.Pool.QueryRow(
		ctx,
		`WITH targets AS (
		     SELECT *
		     FROM unnest($1::uuid[], $2::text[], $3::timestamptz[])
		         AS t(event_id, level, event_time)
		 ),
		 deleted AS (
		     DELETE FROM log_events le
		     USING targets t
		     WHERE le.event_id = t.event_id
		       AND le.level = t.level
		       AND le.event_time = t.event_time
		       AND NOT le.locked
		     RETURNING 1
		 )
		 SELECT
		     (SELECT count(*)
		      FROM log_events le
		      JOIN targets t
		        ON le.event_id = t.event_id
		       AND le.level = t.level
		       AND le.event_time = t.event_time
		      WHERE le.locked),
		     (SELECT count(*) FROM deleted)`,
		eventIDs,
		levels,
		eventTimes,
	).Scan(&result.SkippedLocked, &result.Deleted)
	if err != nil {
		return result, fmt.Errorf("delete selected logs: %w", err)
	}
	return result, nil
}

func (db *DB) DeleteLogsByFilter(ctx context.Context, filter LogFilter) (DeleteResult, error) {
	whereClause, args := buildLogFilterWhere(filter, false)
	return db.deleteLogsByWhere(ctx, db.Pool, whereClause, args)
}

func (db *DB) DeleteLogsByScope(
	ctx context.Context,
	projectKey string,
	nodeKey string,
	level string,
) (DeleteResult, error) {
	if projectKey == "" {
		return DeleteResult{}, fmt.Errorf("project_key is required")
	}

	args := []any{projectKey}
	where := []string{"project_key = $1"}
	if nodeKey != "" {
		args = append(args, nodeKey)
		where = append(where, fmt.Sprintf("node_key = $%d", len(args)))
	}
	if level != "" {
		args = append(args, level)
		where = append(where, fmt.Sprintf("level = $%d", len(args)))
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("begin scoped delete: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	whereClause := "WHERE " + strings.Join(where, " AND ")
	result, err := db.deleteLogsByWhere(ctx, tx, whereClause, args)
	if err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM log_dimensions "+whereClause, args...); err != nil {
		return result, fmt.Errorf("delete log dimensions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit scoped delete: %w", err)
	}
	return result, nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (db *DB) deleteLogsByWhere(
	ctx context.Context,
	querier queryRower,
	whereClause string,
	args []any,
) (DeleteResult, error) {
	lockedWhere := addWhereCondition(whereClause, "locked")
	deleteWhere := addWhereCondition(whereClause, "NOT locked")

	var result DeleteResult
	err := querier.QueryRow(
		ctx,
		`WITH locked AS (
		     SELECT 1 FROM log_events `+lockedWhere+`
		 ),
		 deleted AS (
		     DELETE FROM log_events `+deleteWhere+`
		     RETURNING 1
		 )
		 SELECT
		     (SELECT count(*) FROM locked),
		     (SELECT count(*) FROM deleted)`,
		args...,
	).Scan(&result.SkippedLocked, &result.Deleted)
	if err != nil {
		return result, fmt.Errorf("delete scoped logs: %w", err)
	}
	return result, nil
}

func addWhereCondition(whereClause, condition string) string {
	if whereClause == "" {
		return "WHERE " + condition
	}
	return whereClause + " AND " + condition
}

func (db *DB) SetLogsLocked(
	ctx context.Context,
	items []LogRef,
	locked bool,
) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}

	eventIDs := make([]string, 0, len(items))
	levels := make([]string, 0, len(items))
	eventTimes := make([]time.Time, 0, len(items))
	for _, item := range items {
		eventIDs = append(eventIDs, item.EventID)
		levels = append(levels, item.Level)
		eventTimes = append(eventTimes, item.EventTime)
	}

	result, err := db.Pool.Exec(
		ctx,
		`UPDATE log_events
		 SET locked = $4
		 WHERE (event_id, level, event_time) IN (
		     SELECT * FROM unnest($1::uuid[], $2::text[], $3::timestamptz[])
		 )`,
		eventIDs,
		levels,
		eventTimes,
		locked,
	)
	if err != nil {
		return 0, fmt.Errorf("update log lock state: %w", err)
	}
	return result.RowsAffected(), nil
}
