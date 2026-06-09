-- Down de 001_payments_init.
--
-- CUIDADO: a tabela `payment_gateways` é compartilhada com o monólito em prod.
-- NÃO derrubamos ela aqui (a posse permanece no `viralefy_api`). Apenas
-- desfazemos o que esta migration adicionou (coluna + índices + tabela
-- exclusiva stripe_events_processed). Standalone install que queira limpeza
-- completa pode dropar payment_gateways manualmente.
BEGIN;
DROP INDEX IF EXISTS idx_stripe_events_received;
DROP TABLE IF EXISTS stripe_events_processed;
DROP INDEX IF EXISTS idx_gateways_active_ccy;
ALTER TABLE payment_gateways DROP COLUMN IF EXISTS accepted_currencies;
COMMIT;
