-- 001_payments_init — schema próprio do viralefy_payments.
--
-- Origem: junta as migrations 032 (accepted_currencies) e 035 (stripe_events)
-- do viralefy_api. Como o microsserviço compartilha o Postgres do monólito
-- (Phase 8 §1.3 "sem DB próprio inicial"), esta migration é 100% idempotente:
--
--   * Em prod (DB compartilhado com monólito) → as tabelas/colunas já existem,
--     todos os DDL viram no-op via IF NOT EXISTS.
--   * Em standalone install (DB dedicado ao payments) → cria as tabelas do
--     zero. Mantém compatibilidade com o futuro split de DBs.
--
-- IMPORTANTE: NUNCA tocar o monólito a partir daqui. As tabelas continuam de
-- propriedade conceitual do `viralefy_api`; este script apenas garante que o
-- payments suba sem panic em ambos os cenários.
--
-- proof (032 monólito) NÃO entra aqui — é ciclo de vida da Order, fica no
-- monólito.
BEGIN;

-- ─── payment_gateways (standalone-only; em prod já existe) ─────────────────
-- Mesmo shape que monólito 001_init.up.sql.
CREATE TABLE IF NOT EXISTS payment_gateways (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    provider   TEXT NOT NULL,
    active     BOOLEAN NOT NULL DEFAULT false,
    config     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── accepted_currencies (origem: monólito 032) ────────────────────────────
-- Cada gateway aceita um subconjunto das moedas globais. GIN porque a busca
-- é "gateway que tem X na lista".
ALTER TABLE payment_gateways
    ADD COLUMN IF NOT EXISTS accepted_currencies TEXT[]
    DEFAULT ARRAY['USDT','USD']::TEXT[];

CREATE INDEX IF NOT EXISTS idx_gateways_active_ccy
    ON payment_gateways USING GIN (accepted_currencies)
    WHERE active = TRUE;

-- ─── stripe_events_processed (origem: monólito 035) ────────────────────────
-- Defesa contra double-fire: Stripe re-entrega webhook em 5xx (3 tentativas
-- com backoff). Sem TTL aqui — Stripe re-entrega por até ~3 dias; limpamos
-- via cron de retenção genérico (>90d).
CREATE TABLE IF NOT EXISTS stripe_events_processed (
    event_id    TEXT PRIMARY KEY,
    event_type  TEXT NOT NULL,
    order_id    TEXT,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_stripe_events_received
    ON stripe_events_processed(received_at DESC);

COMMIT;
