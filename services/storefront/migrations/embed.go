package migrations

import "embed"

// Files contains only versioned SQL; changing an applied file prevents startup.
//
//go:embed *.sql
var Files embed.FS
