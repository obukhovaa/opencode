package db

import "embed"

//go:embed migrations/sqlite/*.sql migrations/mysql/*.sql
var FS embed.FS
