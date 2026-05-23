// Package migrations bundles the SQL migration files as an embedded FS so
// the daemon can apply them without needing the migrations/ directory at
// runtime. The goose CLI still works against the files directly when run
// from the project root.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
