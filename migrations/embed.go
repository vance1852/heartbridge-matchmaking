// Package migrations embeds the versioned SQL schema of the service so that a
// single binary can bring an empty database up to the current version without
// shipping loose files next to the executable.
package migrations

import "embed"

// FS holds every versioned migration file, named "<version>_<name>.sql".
//
//go:embed *.sql
var FS embed.FS
