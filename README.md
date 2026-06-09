# viralefy_payments

Microsserviço HTTP interno responsável por toda a integração de gateway de pagamento
(Stripe, Heleket, Woovi, Manual PIX/USDT/Crypto), criação de charges, listagem de
métodos elegíveis e processamento de webhooks externos. Faz parte do carve-out
descrito em
[`viralefy_archive/PHASE-8-MICROSERVICES.md`](../viralefy_archive/PHASE-8-MICROSERVICES.md).

> **Wave 1 scaffolding.** Este repositório ainda é só esqueleto compilável. A
> migração de providers/handlers vem na Wave 2.

## Endpoints (planejados)

Loopback-only (Caddy não expõe ao mundo, exceto rotas de webhook proxied).

```
GET    /internal/health
GET    /internal/methods?plan_id=...&display_currency=...&country=...
POST   /internal/charge
POST   /internal/webhooks/stripe
POST   /internal/webhooks/heleket
POST   /internal/webhooks/woovi
GET    /internal/gateways
POST   /internal/gateways
PUT    /internal/gateways/{id}
DELETE /internal/gateways/{id}
```

Wave 1 só expõe `/internal/health`. O resto vem na Wave 2.

## Porta / bind

| Var               | Default       | Observação                              |
|-------------------|---------------|------------------------------------------|
| `PAYMENTS_PORT`   | `8081`        | Porta TCP                                |
| `BIND_HOST`       | `127.0.0.1`   | Loopback-only por padrão                 |

A API principal (`viralefy_api`) é o único cliente e fala via
`http://127.0.0.1:8081`.

## Variáveis de ambiente

| Var                       | Obrigatória | Descrição                                                        |
|---------------------------|-------------|------------------------------------------------------------------|
| `DATABASE_URL`            | sim         | Postgres compartilhado (schema `payments.*` futuramente).        |
| `INTERNAL_SHARED_SECRET`  | sim         | Token de autenticação loopback (`X-Internal-Token`).             |
| `API_CALLBACK_URL`        | sim         | URL do `viralefy_api` pra callback `/internal/payment-confirmed`.|
| `SITE_URL`                | recomendada | URL pública da loja, para montar return URLs do Stripe/etc.      |
| `SENTRY_DSN`              | não         | Vazio = Sentry no-op.                                            |
| `PAYMENTS_PORT`           | não         | Default `8081`.                                                  |
| `BIND_HOST`               | não         | Default `127.0.0.1`.                                             |
| `APP_VERSION`             | não         | Injetada por `-ldflags` no release; default `dev`.                |
| `APP_ENV`                 | não         | `production` / `staging` / `dev`. Passado pro Sentry.            |

## Auth interno

Toda request `/internal/*` (exceto `/internal/health` e webhooks públicos
proxiados pela API) exige o header:

```
X-Internal-Token: <INTERNAL_SHARED_SECRET>
```

O segredo é gerado pelo installer do `viralefy_ops` e persistido em
`/etc/viralefy/.env`. Defense-in-depth pra mitigar bypass acidental do
loopback (containers, port-forward etc).

## Rodar local

```bash
export DATABASE_URL="postgres://viralefy:viralefy@localhost:15432/viralefy?sslmode=disable"
export INTERNAL_SHARED_SECRET="dev-only-shared-secret-min-16-chars"
export API_CALLBACK_URL="http://127.0.0.1:8080"
go run ./cmd/payments
```

Health:

```bash
curl -sS http://127.0.0.1:8081/internal/health
# {"status":"ok"}
```

## Build

```bash
go build ./...
```

## Estrutura

```
viralefy_payments/
├── cmd/payments/         # entrypoint (HTTP server)
├── internal/
│   ├── config/           # Load() de env vars
│   ├── domain/           # entidades (Gateway, PaymentMethod) — Wave 2
│   ├── application/      # services (charge, methods, eligibility) — Wave 2
│   ├── infrastructure/
│   │   ├── external/payment/      # providers Stripe/Heleket/Woovi — Wave 2
│   │   └── persistence/postgres/  # pool + repos
│   └── interface/http/   # router, middlewares, handlers
└── README.md
```

## Roadmap

1. **Wave 1 (este commit).** Scaffolding compilável, `/internal/health` ok.
2. **Wave 2.** Mover providers + `application/payment*` + `gateway_service` +
   migrations 032/034/035 do monolito pra cá.
3. **Wave 3.** Substituir chamadas in-memory no monolito por HTTP client.
4. **Wave 4.** Dashboards Grafana + alerts.

Plano completo: [`PHASE-8-MICROSERVICES.md`](../viralefy_archive/PHASE-8-MICROSERVICES.md).
