package domain

// Currency é o subconjunto da tabela `currencies` que esse microsserviço
// precisa (read-only) pra computar amounts por moeda de pay-in.
// Mantemos só o que payment_methods + checkout precisam — o serviço completo
// (com cascade em plan_prices) continua sendo dono do viralefy_api.
type Currency struct {
	Code           string  `json:"code"`
	Symbol         string  `json:"symbol"`
	Rate           float64 `json:"rate"`
	Decimals       int     `json:"decimals"`
	SettlementCode string  `json:"settlement_code"`
}

// Plan é um snapshot mínimo do plano usado pra computar amounts. Inclui
// PriceCents + Prices manuais por moeda (mesma semântica do monólito).
type Plan struct {
	ID         string
	Name       string
	Active     bool
	PriceCents int
	Prices     map[string]string
}
