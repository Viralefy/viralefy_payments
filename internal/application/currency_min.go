package application

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Viralefy/viralefy_payments/internal/domain"
)

// CurrencyReader é um leitor mínimo da tabela `currencies` compartilhada.
// Não cobre cascade de plan_prices nem Update — o monólito continua dono
// disso. Aqui só precisamos do que basta pra computar amounts por moeda
// nos endpoints de methods + charge.
type CurrencyReader struct {
	pool *pgxpool.Pool
}

func NewCurrencyReader(pool *pgxpool.Pool) *CurrencyReader {
	return &CurrencyReader{pool: pool}
}

// GetByCode devolve a moeda pelo código (case-insensitive). Retorna
// domain.ErrNotFound quando o code não existe.
func (r *CurrencyReader) GetByCode(ctx context.Context, code string) (*domain.Currency, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, domain.ErrInvalidInput
	}
	row := r.pool.QueryRow(ctx, `
		SELECT code, symbol, rate, decimals, settlement_code
		FROM currencies WHERE code=$1`, code)
	var c domain.Currency
	if err := row.Scan(&c.Code, &c.Symbol, &c.Rate, &c.Decimals, &c.SettlementCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// amountFor é a mesma lógica do monólito: se há preço manual no map prices
// pra essa moeda, usa; senão converte do USD usando o rate.
func amountFor(prices map[string]string, usdCents int, c domain.Currency) string {
	if v, ok := prices[c.Code]; ok && v != "" {
		return v
	}
	amount := float64(usdCents) / 100.0 * c.Rate
	return strconv.FormatFloat(amount, 'f', c.Decimals, 64)
}
