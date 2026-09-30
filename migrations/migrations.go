// Package migrations embeds the versioned SQL migration files
// (NNNN_name.up.sql / NNNN_name.down.sql) applied at boot by internal/migrate.
package migrations

import "embed"

// FS holds every *.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
