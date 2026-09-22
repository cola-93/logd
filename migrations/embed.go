package migrations

import "embed"

// Files contains the ordered PostgreSQL migration files.
//
//go:embed *.sql
var Files embed.FS
