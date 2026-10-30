// Package sqlite is the orchestrator's database: a modernc.org/sqlite pool, goose migrations
// embedded from migrations/, and sqlc-generated queries in the db subpackage. sqlc row types
// never leave this package; exported Store methods return homelab types.
package sqlite

//go:generate go tool sqlc generate -f ../../sqlc.yaml
