package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectTokenRecord struct {
	ID         string    `json:"id"`
	ProjectKey string    `json:"project_key"`
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ProjectTokenUpdate struct {
	ProjectKey *string
	Name       *string
	Enabled    *bool
}

func (db *DB) ListProjectTokens(ctx context.Context) ([]ProjectTokenRecord, error) {
	rows, err := db.Pool.Query(
		ctx,
		`SELECT id::text, project_key, name, enabled, created_at, updated_at
		 FROM project_tokens
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list project tokens: %w", err)
	}
	defer rows.Close()

	var tokens []ProjectTokenRecord
	for rows.Next() {
		var token ProjectTokenRecord
		if err := rows.Scan(
			&token.ID,
			&token.ProjectKey,
			&token.Name,
			&token.Enabled,
			&token.CreatedAt,
			&token.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project tokens: %w", err)
	}
	return tokens, nil
}

func (db *DB) CreateProjectToken(
	ctx context.Context,
	id string,
	projectKey string,
	name string,
	tokenHash string,
	enabled bool,
) (ProjectTokenRecord, error) {
	var token ProjectTokenRecord
	err := db.Pool.QueryRow(
		ctx,
		`INSERT INTO project_tokens (id, project_key, name, token_hash, enabled)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING id::text, project_key, name, enabled, created_at, updated_at`,
		id,
		projectKey,
		name,
		tokenHash,
		enabled,
	).Scan(
		&token.ID,
		&token.ProjectKey,
		&token.Name,
		&token.Enabled,
		&token.CreatedAt,
		&token.UpdatedAt,
	)
	if err != nil {
		return token, fmt.Errorf("create project token: %w", err)
	}
	return token, nil
}

func (db *DB) UpdateProjectToken(
	ctx context.Context,
	id string,
	update ProjectTokenUpdate,
) (ProjectTokenRecord, error) {
	var token ProjectTokenRecord
	err := db.Pool.QueryRow(
		ctx,
		`UPDATE project_tokens
		 SET project_key = COALESCE($2::text, project_key),
		     name = COALESCE($3::text, name),
		     enabled = COALESCE($4::boolean, enabled),
		     updated_at = now()
		 WHERE id = $1::uuid
		 RETURNING id::text, project_key, name, enabled, created_at, updated_at`,
		id,
		update.ProjectKey,
		update.Name,
		update.Enabled,
	).Scan(
		&token.ID,
		&token.ProjectKey,
		&token.Name,
		&token.Enabled,
		&token.CreatedAt,
		&token.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return token, pgx.ErrNoRows
		}
		return token, fmt.Errorf("update project token: %w", err)
	}
	return token, nil
}

func (db *DB) DeleteProjectToken(ctx context.Context, id string) (bool, error) {
	result, err := db.Pool.Exec(
		ctx,
		`DELETE FROM project_tokens WHERE id = $1::uuid`,
		id,
	)
	if err != nil {
		return false, fmt.Errorf("delete project token: %w", err)
	}
	return result.RowsAffected() > 0, nil
}
