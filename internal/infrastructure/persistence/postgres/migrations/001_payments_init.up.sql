-- 001_payments_init — schema próprio do viralefy_payments.
--
-- Origem: junta as migrations 032 (accepted_currencies) e 035 (stripe_events)
-- do viralefy_api. Como o microsserviço compartilha o Postgres do monólito
-- (Phase 8 §1.3 "sem DB próprio inicial"), aqui só ALTERAMOS as tabelas que
-- já existem (idempotente via IF NOT EXISTS) e criamos as novas que são
-- escopo exclusivo do payments service.
--
-- payment_gateways em si continua sendo criada por 001_init do monólito;
-- migrations 032 (proof) e 034 (proof storage) NÃO vêm pra cá — proof
-- segue no monólito porque é parte do ciclo de vida da Order.
BEGIN;

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
