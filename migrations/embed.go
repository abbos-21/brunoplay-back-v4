// Package migrations embeds the SQL migrations so the single binary can migrate its own database.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
