package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type LogFilter struct {
	StartTime  time.Time
	EndTime    time.Time
	ProjectKey string
	NodeKey    string
	Level      string
	ErrorScene string
	RequestIP  string
	Member     string
	SessionID  string
	Limit      int
	CursorTime time.Time
	CursorID   string
}

type LogSummary struct {
	EventID       string    `json:"event_id"`
	EventTime     time.Time `json:"event_time"`
	ReceivedAt    time.Time `json:"received_at"`
	ProjectKey    string    `json:"project_key"`
	NodeKey       string    `json:"node_key"`
	Level         string    `json:"level"`
	RequestIP     string    `json:"request_ip"`
	Member        string    `json:"member"`
	SessionID     string    `json:"session_id"`
	RequestMethod string    `json:"request_method"`
	RequestURL    string    `json:"request_url"`
	ErrorScene    string    `json:"error_scene"`
	ErrorMessage  string    `json:"error_message"`
	Locked        bool      `json:"locked"`
}

type LogDetail struct {
	LogSummary
	RequestHeaders json.RawMessage `json:"request_headers"`
	RequestParams  json.RawMessage `json:"request_params"`
	ErrorFile      *string         `json:"error_file"`
	ErrorLine      *int            `json:"error_line"`
	ErrorStack     *string         `json:"error_stack"`
}

func (db *DB) QueryLogs(
	ctx context.Context,
	filter LogFilter,
) ([]LogSummary, bool, error) {
	whereClause, args := buildLogFilterWhere(filter, true)

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit+1)

	query := fmt.Sprintf(`
		SELECT event_id::text,
		       event_time,
		       received_at,
		       project_key,
		       node_key,
		       level,
		       host(request_ip),
		       member,
		       session_id,
		       request_method,
		       request_url,
		       error_scene,
		       error_message,
		       locked
		FROM log_events
		%s
		ORDER BY event_time DESC, event_id DESC
		LIMIT $%d
	`, whereClause, len(args))

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	items := make([]LogSummary, 0, limit+1)
	for rows.Next() {
		var item LogSummary
		if err := rows.Scan(
			&item.EventID,
			&item.EventTime,
			&item.ReceivedAt,
			&item.ProjectKey,
			&item.NodeKey,
			&item.Level,
			&item.RequestIP,
			&item.Member,
			&item.SessionID,
			&item.RequestMethod,
			&item.RequestURL,
			&item.ErrorScene,
			&item.ErrorMessage,
			&item.Locked,
		); err != nil {
			return nil, false, fmt.Errorf("scan log summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate log summaries: %w", err)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func buildLogFilterWhere(filter LogFilter, includeCursor bool) (string, []any) {
	var where []string
	var args []any

	if !filter.StartTime.IsZero() {
		args = append(args, filter.StartTime)
		where = append(where, fmt.Sprintf("event_time >= $%d", len(args)))
	}
	if !filter.EndTime.IsZero() {
		args = append(args, filter.EndTime)
		where = append(where, fmt.Sprintf("event_time < $%d", len(args)))
	}

	addStringFilter := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		where = append(where, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	addContainsFilter := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, "%"+value+"%")
		where = append(where, fmt.Sprintf("%s ILIKE $%d", column, len(args)))
	}
	addStringFilter("project_key", filter.ProjectKey)
	addContainsFilter("node_key", filter.NodeKey)
	addStringFilter("level", filter.Level)
	addContainsFilter("error_scene", filter.ErrorScene)
	addStringFilter("member", filter.Member)
	addStringFilter("session_id", filter.SessionID)
	if filter.RequestIP != "" {
		args = append(args, filter.RequestIP)
		where = append(where, fmt.Sprintf("request_ip = $%d::inet", len(args)))
	}
	if includeCursor && !filter.CursorTime.IsZero() {
		args = append(args, filter.CursorTime)
		timeParam := len(args)
		args = append(args, filter.CursorID)
		idParam := len(args)
		where = append(
			where,
			fmt.Sprintf("(event_time, event_id) < ($%d, $%d::uuid)", timeParam, idParam),
		)
	}

	if len(where) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

func (db *DB) LogDetail(
	ctx context.Context,
	eventID string,
	level string,
	eventTime time.Time,
) (LogDetail, error) {
	var item LogDetail
	err := db.Pool.QueryRow(
		ctx,
		`SELECT event_id::text,
		        event_time,
		        received_at,
		        project_key,
		        node_key,
		        level,
		        host(request_ip),
		        member,
		        session_id,
		        request_method,
		        request_url,
		        error_scene,
		        error_message,
		        locked,
		        request_headers,
		        request_params,
		        error_file,
		        error_line,
		        error_stack
		 FROM log_events
		 WHERE event_id = $1::uuid
		   AND level = $2
		   AND event_time = $3`,
		eventID,
		level,
		eventTime,
	).Scan(
		&item.EventID,
		&item.EventTime,
		&item.ReceivedAt,
		&item.ProjectKey,
		&item.NodeKey,
		&item.Level,
		&item.RequestIP,
		&item.Member,
		&item.SessionID,
		&item.RequestMethod,
		&item.RequestURL,
		&item.ErrorScene,
		&item.ErrorMessage,
		&item.Locked,
		&item.RequestHeaders,
		&item.RequestParams,
		&item.ErrorFile,
		&item.ErrorLine,
		&item.ErrorStack,
	)
	if err != nil {
		return item, fmt.Errorf("load log detail: %w", err)
	}
	return item, nil
}

type LogHierarchyItem struct {
	ProjectKey string `json:"project_key"`
	NodeKey    string `json:"node_key"`
	Level      string `json:"level"`
}

func (db *DB) LogHierarchy(ctx context.Context) ([]LogHierarchyItem, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT project_key, node_key, level
		FROM log_dimensions
		ORDER BY project_key, node_key, level
	`)
	if err != nil {
		return nil, fmt.Errorf("query log hierarchy: %w", err)
	}
	defer rows.Close()

	var items []LogHierarchyItem
	for rows.Next() {
		var item LogHierarchyItem
		if err := rows.Scan(&item.ProjectKey, &item.NodeKey, &item.Level); err != nil {
			return nil, fmt.Errorf("scan log hierarchy: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate log hierarchy: %w", err)
	}
	return items, nil
}
