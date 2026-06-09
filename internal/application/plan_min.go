package application

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Viralefy/viralefy_payments/internal/domain"
)

// PlanReader é o leitor mínimo da tabela `plans` + `plan_prices`. Mesmo
// motivo do CurrencyReader: o monólito continua dono do CRUD, aqui só
// fazemos read pra montar os PaymentMethodOption + computar charge amount.
type PlanReader struct {
	pool *pgxpool.Pool
}

func NewPlanReader(pool *pgxpool.Pool) *PlanReader {
	return &PlanReader{pool: pool}
}

// GetByID hidrata o plano + map de preços manuais (currency → amount).
// Retorna ErrNotFound quando o plan id não existe.
func (r *PlanReader) GetByID(ctx context.Context, id string) (*domain.Plan, error) {
	var p domain.Plan
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, active, price_cents
		FROM plans WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Active, &p.PriceCents)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT currency_code, amount FROM plan_prices WHERE plan_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p.Prices = map[string]string{}
	for rows.Next() {
		var code, amount string
		if err := rows.Scan(&code, &amount); err != nil {
			return nil, err
		}
		p.Prices[code] = amount
	}
	return &p, rows.Err()
}
