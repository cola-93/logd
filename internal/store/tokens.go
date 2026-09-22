package store

import (
	"context"
	"fmt"
	"sync"
)

type ProjectToken struct {
	TokenHash  string
	ProjectKey string
	Enabled    bool
}

type TokenCache struct {
	mu     sync.RWMutex
	tokens map[string]ProjectToken
}

func (db *DB) LoadTokenCache(ctx context.Context) (*TokenCache, error) {
	cache := &TokenCache{}
	if err := db.RefreshTokenCache(ctx, cache); err != nil {
		return nil, err
	}
	return cache, nil
}

func (db *DB) RefreshTokenCache(ctx context.Context, cache *TokenCache) error {
	rows, err := db.Pool.Query(
		ctx,
		`SELECT token_hash, project_key, enabled FROM project_tokens`,
	)
	if err != nil {
		return fmt.Errorf("load project tokens: %w", err)
	}
	defer rows.Close()

	tokens := make(map[string]ProjectToken)
	for rows.Next() {
		var token ProjectToken
		if err := rows.Scan(&token.TokenHash, &token.ProjectKey, &token.Enabled); err != nil {
			return fmt.Errorf("scan project token: %w", err)
		}
		tokens[token.TokenHash] = token
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate project tokens: %w", err)
	}

	cache.mu.Lock()
	cache.tokens = tokens
	cache.mu.Unlock()
	return nil
}

func (c *TokenCache) Lookup(hash string) (ProjectToken, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	token, ok := c.tokens[hash]
	return token, ok
}
