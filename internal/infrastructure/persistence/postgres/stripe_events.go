package postgres

import "context"

// StripeEventsRepo é o ledger de idempotência dos webhooks Stripe. Stripe
// re-entrega webhook em 5xx (3 tentativas com backoff); guard aqui evita
// duplicate-fire do callback `/internal/payment-confirmed`.
type StripeEventsRepo struct{ db *DB }

func NewStripeEventsRepo(db *DB) *StripeEventsRepo { return &StripeEventsRepo{db: db} }

// Record marca um event_id como processado. Retorna inserted=true na
// primeira vez, false quando a row já existia (replay → noop). Implementado
// com INSERT ... ON CONFLICT DO NOTHING pra ser atômico sem precisar de
// SELECT prévio (race contra duas entregas concorrentes do Stripe).
func (r *StripeEventsRepo) Record(ctx context.Context, eventID, eventType, orderID string) (bool, error) {
	tag, err := r.db.pool.Exec(ctx, `
		INSERT INTO stripe_events_processed (event_id, event_type, order_id)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType, orderID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
