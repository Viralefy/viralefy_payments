package application

import (
	"context"
	"strings"

	"github.com/Viralefy/viralefy_payments/internal/domain"
)

// PaymentMethodOption descreve um método de pagamento DISPONÍVEL pra um
// pedido específico. O cliente vê uma lista desses cards no checkout e
// escolhe um. Shape espelha o do monólito 1:1 — qualquer divergência
// quebra a UI que já consome esse JSON.
type PaymentMethodOption struct {
	GatewayID          string `json:"gateway_id"`
	Provider           string `json:"provider"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ChargedCurrency    string `json:"charged_currency"`
	ChargedAmount      string `json:"charged_amount"`
	ChargedSymbol      string `json:"charged_symbol"`
	SettlementCurrency string `json:"settlement_currency"`
	SettlementAmount   string `json:"settlement_amount"`
	SettlementSymbol   string `json:"settlement_symbol"`
	DisplayCurrency    string `json:"display_currency"`
	DisplayAmount      string `json:"display_amount"`
	ConversionNote     string `json:"conversion_note,omitempty"`
	NetworkLabel       string `json:"network_label,omitempty"`
	NetworkWarning     string `json:"network_warning,omitempty"`
}

// quote é o subset interno do monólito.Quote — display + settlement currency
// e amounts já resolvidos. Mantemos local pra não acoplar com CurrencyService
// completo (esse serviço só lê DB).
type quote struct {
	DisplayCurrency    string
	DisplaySymbol      string
	DisplayAmount      string
	SettlementCurrency string
	SettlementSymbol   string
	SettlementAmount   string
}

// MethodsService monta a lista de métodos de pagamento disponíveis para um
// plano + display currency + country. É o read-side puro do checkout —
// não cria order, só monta o catálogo.
type MethodsService struct {
	plans      *PlanReader
	currencies *CurrencyReader
	gateways   *GatewayService
}

func NewMethodsService(plans *PlanReader, cur *CurrencyReader, gws *GatewayService) *MethodsService {
	return &MethodsService{plans: plans, currencies: cur, gateways: gws}
}

// ListMethods retorna os métodos de pagamento aceitos pra um plano, já com
// o preview de quanto o cliente paga em cada gateway. Não cria pedido — é
// só o catálogo pra UI montar a lista de cards. Mesmo algoritmo do monólito.
func (s *MethodsService) ListMethods(ctx context.Context, planID, displayCurrency, country string) ([]PaymentMethodOption, error) {
	plan, err := s.plans.GetByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	if !plan.Active {
		return nil, domain.ErrInvalidInput
	}
	q, err := s.quoteForPlan(ctx, plan, displayCurrency)
	if err != nil {
		return nil, err
	}
	all, err := s.gateways.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PaymentMethodOption, 0, len(all))
	for _, g := range all {
		if !g.Active {
			continue
		}
		if !gatewayEligible(g, q.DisplayCurrency, q.SettlementCurrency, country) {
			continue
		}
		out = append(out, s.buildMethodOptions(ctx, g, plan, q)...)
	}
	return out, nil
}

// quoteForPlan é a mesma lógica do CurrencyService.QuoteForPlan do monólito,
// usando o CurrencyReader local. Display inválido cai em USD.
func (s *MethodsService) quoteForPlan(ctx context.Context, plan *domain.Plan, displayCode string) (quote, error) {
	if displayCode == "" {
		displayCode = "USD"
	}
	display, err := s.currencies.GetByCode(ctx, displayCode)
	if err != nil || display == nil {
		display, err = s.currencies.GetByCode(ctx, "USD")
		if err != nil {
			return quote{}, err
		}
	}
	settle, err := s.currencies.GetByCode(ctx, display.SettlementCode)
	if err != nil || settle == nil {
		settle = display
	}
	return quote{
		DisplayCurrency:    display.Code,
		DisplaySymbol:      display.Symbol,
		DisplayAmount:      amountFor(plan.Prices, plan.PriceCents, *display),
		SettlementCurrency: settle.Code,
		SettlementSymbol:   settle.Symbol,
		SettlementAmount:   amountFor(plan.Prices, plan.PriceCents, *settle),
	}, nil
}

// AmountInCurrency calcula o valor de um plano em uma moeda específica.
// Usado pelo charge handler quando o cliente escolhe uma moeda de pay-in
// (Heleket/Stripe multi-currency). ok=false quando a moeda não está cadastrada.
func (s *MethodsService) AmountInCurrency(ctx context.Context, plan *domain.Plan, currencyCode string) (string, string, bool) {
	code := strings.ToUpper(strings.TrimSpace(currencyCode))
	if code == "" {
		return "", "", false
	}
	cur, err := s.currencies.GetByCode(ctx, code)
	if err != nil || cur == nil {
		return "", "", false
	}
	return amountFor(plan.Prices, plan.PriceCents, *cur), code, true
}

// multiCurrencyProviders — providers onde 1 gateway = N opções de pay-in.
// Heleket aceita BTC/ETH/USDT/LTC; Stripe pode rodar em USD/EUR/BRL/GBP.
var multiCurrencyProviders = map[string]bool{
	"heleket": true,
	"stripe":  true,
}

// cryptoProviders são os providers que efetivamente cobram em crypto —
// recebem o passe USDT universal porque a conversão é resolvida internamente.
// Fiat providers (PIX/Stripe) NÃO recebem — PIX literalmente só cobra BRL.
var cryptoProviders = map[string]bool{
	"manual_crypto": true,
	"manual_usdt":   true,
	"heleket":       true,
}

// brOnlyProviders — providers que SÓ fazem sentido pra cliente brasileiro.
// PIX é rail doméstico; alemão em EUR não tem como gerar PIX. REGRA HARD:
// esses providers SÓ aparecem se country=="br". Display=BRL pra alemão NÃO
// é suficiente — currency é preferência, não nacionalidade.
var brOnlyProviders = map[string]bool{
	"manual_pix": true,
	"woovi":      true,
	"abacatepay": true, // PIX é rail BR-only, processor não muda isso
}

// gatewayEligible decide se um gateway deve aparecer pro cliente. Regras
// em ordem de precedência:
//
//  1. brOnlyProviders: country=br obrigatório. Esconde sem country.
//  2. cryptoProviders com USDT: UNIVERSAL — qualquer display.
//  3. Display ou settlement currency em accepted_currencies → mostra.
//  4. Qualquer outro caso → esconde.
func gatewayEligible(g domain.PaymentGateway, displayCurrency, settlementCurrency, country string) bool {
	display := strings.ToUpper(strings.TrimSpace(displayCurrency))
	settle := strings.ToUpper(strings.TrimSpace(settlementCurrency))
	country = strings.ToLower(strings.TrimSpace(country))
	provider := strings.ToLower(strings.TrimSpace(g.Provider))

	if brOnlyProviders[provider] {
		return country == "br"
	}

	isCrypto := cryptoProviders[provider]
	for _, raw := range g.AcceptedCurrencies {
		c := strings.ToUpper(strings.TrimSpace(raw))
		if c == "USDT" && isCrypto {
			return true
		}
		if c == display || c == settle {
			return true
		}
	}
	return false
}

// buildMethodOptions emite SEMPRE UM card por gateway. Multi-currency
// providers (Stripe/Heleket) já não expandem em N cards — escolhemos a
// moeda primária do gateway e mostramos a conversão pra moeda display via
// `conversion_note`. Decisão de produto: cliente quer UMA opção visível por
// método, sem a poluição de "Stripe pay in USD / pay in BRL / pay in EUR…"
// que a versão anterior gerava.
//
// Regra de escolha da moeda primária:
//   - Stripe (fiat multi): se display ∈ accepted → cobra na display (sem
//     conversion_note); senão USD se aceita; senão primeira aceita.
//   - Heleket (crypto multi): prefere USDT (estável + universal); senão
//     primeira aceita.
//   - Single-currency: usa a única moeda aceita do gateway.
func (s *MethodsService) buildMethodOptions(ctx context.Context, g domain.PaymentGateway, plan *domain.Plan, q quote) []PaymentMethodOption {
	if len(g.AcceptedCurrencies) == 0 {
		return nil
	}
	code := pickPrimaryCurrency(g, q.DisplayCurrency)
	if code == "" {
		return nil
	}
	if opt, ok := s.buildSingleOption(ctx, g, plan, q, code); ok {
		return []PaymentMethodOption{opt}
	}
	return nil
}

// pickPrimaryCurrency centraliza a heurística de "qual moeda do gateway o
// cliente vê". Igualdade case-insensitive em accepted_currencies — admins
// cadastram em qualquer caixa.
func pickPrimaryCurrency(g domain.PaymentGateway, displayCurrency string) string {
	display := strings.ToUpper(strings.TrimSpace(displayCurrency))
	provider := strings.ToLower(strings.TrimSpace(g.Provider))
	accepted := make([]string, 0, len(g.AcceptedCurrencies))
	for _, raw := range g.AcceptedCurrencies {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code != "" {
			accepted = append(accepted, code)
		}
	}
	if len(accepted) == 0 {
		return ""
	}
	contains := func(code string) bool {
		for _, c := range accepted {
			if c == code {
				return true
			}
		}
		return false
	}
	if !multiCurrencyProviders[provider] {
		return accepted[0]
	}
	// Heleket (crypto) → USDT primeiro (única stable do pool, evita
	// volatilidade de fechamento na ponta do cliente).
	if cryptoProviders[provider] && contains("USDT") {
		return "USDT"
	}
	// Stripe (fiat) → prefere a moeda do display se aceita.
	if display != "" && contains(display) {
		return display
	}
	// Fallback genérico: USD se aceito (cobre Stripe sem display match), senão
	// a primeira moeda do array.
	if contains("USD") {
		return "USD"
	}
	return accepted[0]
}

// buildSingleOption emite UM PaymentMethodOption pra (gateway, chargedCurrency).
// Centraliza conversion_note + network warning (crypto).
func (s *MethodsService) buildSingleOption(ctx context.Context, g domain.PaymentGateway, plan *domain.Plan, q quote, chargedCurrency string) (PaymentMethodOption, bool) {
	cur, err := s.currencies.GetByCode(ctx, chargedCurrency)
	if err != nil || cur == nil {
		return PaymentMethodOption{}, false
	}
	chargedAmount := amountFor(plan.Prices, plan.PriceCents, *cur)
	settleCurrency := cur.SettlementCode
	if settleCurrency == "" {
		settleCurrency = q.SettlementCurrency
	}
	settleCurrency = strings.ToUpper(strings.TrimSpace(settleCurrency))
	settleAmount := chargedAmount
	settleSymbol := cur.Symbol
	if !strings.EqualFold(chargedCurrency, settleCurrency) {
		settleCur, err := s.currencies.GetByCode(ctx, settleCurrency)
		if err == nil && settleCur != nil {
			settleAmount = amountFor(plan.Prices, plan.PriceCents, *settleCur)
			settleSymbol = settleCur.Symbol
		} else {
			settleAmount = q.SettlementAmount
			settleSymbol = q.SettlementSymbol
		}
	}
	// Agora que cada gateway emite UM card, não há ambiguidade entre opções
	// do mesmo provider — `name` é o label puro do gateway. A moeda cobrada
	// aparece em charged_amount/charged_currency + conversion_note.
	name := g.Name
	opt := PaymentMethodOption{
		GatewayID:          g.ID,
		Provider:           g.Provider,
		Name:               name,
		Kind:               kindOf(g.Provider),
		ChargedCurrency:    chargedCurrency,
		ChargedAmount:      chargedAmount,
		ChargedSymbol:      cur.Symbol,
		SettlementCurrency: settleCurrency,
		SettlementAmount:   settleAmount,
		SettlementSymbol:   settleSymbol,
		DisplayCurrency:    q.DisplayCurrency,
		DisplayAmount:      q.DisplayAmount,
	}
	if !strings.EqualFold(chargedCurrency, q.DisplayCurrency) {
		opt.ConversionNote = "Price shown: " + q.DisplaySymbol + " " + q.DisplayAmount +
			" " + q.DisplayCurrency + ". You pay " + cur.Symbol + " " + chargedAmount +
			" " + chargedCurrency + " — platform settles in " + settleCurrency +
			" (" + settleAmount + " " + settleCurrency + ")."
	} else if !strings.EqualFold(chargedCurrency, settleCurrency) {
		opt.ConversionNote = "You pay " + cur.Symbol + " " + chargedAmount + " " + chargedCurrency +
			"; platform receives " + settleAmount + " " + settleCurrency + " after auto-conversion."
	}
	if g.Provider == "manual_crypto" || g.Provider == "manual_usdt" {
		if net := strings.TrimSpace(g.Config["network"]); net != "" {
			opt.NetworkLabel = strings.TrimSpace(g.Config["network_label"])
			if opt.NetworkLabel == "" {
				opt.NetworkLabel = chargedCurrency + " (" + net + ")"
			}
			opt.NetworkWarning = strings.TrimSpace(g.Config["network_warning"])
			if opt.NetworkWarning == "" {
				opt.NetworkWarning = "Send ONLY on the " + net +
					" network. Deposits on any other network will be lost forever."
			}
		}
	}
	return opt, true
}

// gwAccepts verifica se um gateway tem currency code em accepted_currencies.
// Case-insensitive, trim. Defense in depth contra payload arbitrário.
func gwAccepts(g *domain.PaymentGateway, code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range g.AcceptedCurrencies {
		if strings.ToUpper(strings.TrimSpace(c)) == code {
			return true
		}
	}
	return false
}

// pickChargedCurrency escolhe a moeda em que o gateway efetivamente cobra.
// Se aceita settlement (USDT na maioria) → usa; senão pega a primeira.
func pickChargedCurrency(accepted []string, settlement string) string {
	settlement = strings.ToUpper(settlement)
	for _, c := range accepted {
		if strings.ToUpper(c) == settlement {
			return settlement
		}
	}
	return strings.ToUpper(strings.TrimSpace(accepted[0]))
}

// kindOf mapeia provider → kind genérico (UI usa pra ícone/etiqueta).
func kindOf(provider string) string {
	switch provider {
	case "woovi", "manual_pix", "abacatepay":
		return "pix"
	case "stripe":
		return "card"
	case "manual_crypto", "manual_usdt":
		return "crypto_manual"
	case "heleket":
		return "crypto_auto"
	}
	return "other"
}
