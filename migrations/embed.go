// Package migrations holds the numbered SQL files, embedded in the binary.
package migrations

import "embed"

// FS holds main/*.sql and site/*.sql.
//
//go:embed main/*.sql site/*.sql
var FS embed.FS
