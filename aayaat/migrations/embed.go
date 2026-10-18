// Package migrations embeds the SQL migrations so the binary carries them.
package migrations

import "embed"

// FS holds *.up.sql files, applied in name order at startup when AUTO_MIGRATE=true.
//
//go:embed *.sql
var FS embed.FS
