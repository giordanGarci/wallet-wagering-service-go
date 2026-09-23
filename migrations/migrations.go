// Package migrations embeds the versioned SQL migrations.
//
// Files follow the golang-migrate naming scheme
// NNNNNN_description.up.sql / NNNNNN_description.down.sql. In the compose
// environment the `migrate` service (migrate/migrate CLI) applies them from
// this directory; the integration harness applies the same files from FS.
package migrations

import "embed"

// FS holds every *.sql file of this directory.
//
//go:embed *.sql
var FS embed.FS
