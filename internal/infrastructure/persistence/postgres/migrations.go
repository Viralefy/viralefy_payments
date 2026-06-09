package postgres

import (
	"context"
	_ "embed"
)

// migration001Up é o conteúdo da migration de bootstrap (accepted_currencies
// + stripe_events_processed). Embed do .sql ao lado pra evitar I/O em boot
// e manter o binário self-contained.
//
//go:embed migrations/001_payments_init.up.sql
var migration001Up string

// ApplyMigrations roda todas as migrations idempotentes do payments service.
// Falha aqui é tratada como aviso pelo caller — todas as DDLs usam IF NOT
// EXISTS, então re-run é seguro.
func (d *DB) ApplyMigrations(ctx context.Context) error {
	return d.Migrate(ctx, migration001Up)
}
