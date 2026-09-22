package store

import (
	"context"
	"fmt"
)

type SystemSettings struct {
	RetentionDays int `json:"retention_days"`
}

func (db *DB) RetentionDays(ctx context.Context) (int, error) {
	settings, err := db.SystemSettings(ctx)
	if err != nil {
		return 0, err
	}
	return settings.RetentionDays, nil
}

func (db *DB) SystemSettings(ctx context.Context) (SystemSettings, error) {
	var settings SystemSettings
	err := db.Pool.QueryRow(
		ctx,
		`SELECT (setting_value #>> '{}')::int
		 FROM system_settings
		 WHERE setting_key = 'retention_days'`,
	).Scan(&settings.RetentionDays)
	if err != nil {
		return settings, fmt.Errorf("load system settings: %w", err)
	}
	if settings.RetentionDays < 1 {
		return settings, fmt.Errorf("retention_days must be at least 1")
	}
	return settings, nil
}

func (db *DB) UpdateSystemSettings(
	ctx context.Context,
	retentionDays *int,
) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin settings update: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if retentionDays != nil {
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO system_settings (setting_key, setting_value, updated_at)
			 VALUES ('retention_days', to_jsonb($1::int), now())
			 ON CONFLICT (setting_key) DO UPDATE
			 SET setting_value = EXCLUDED.setting_value,
			     updated_at = now()`,
			*retentionDays,
		); err != nil {
			return fmt.Errorf("update retention_days: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit settings update: %w", err)
	}
	return nil
}
