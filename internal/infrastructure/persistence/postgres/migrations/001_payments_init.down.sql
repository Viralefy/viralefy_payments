BEGIN;
DROP INDEX IF EXISTS idx_stripe_events_received;
DROP TABLE IF EXISTS stripe_events_processed;
DROP INDEX IF EXISTS idx_gateways_active_ccy;
ALTER TABLE payment_gateways DROP COLUMN IF EXISTS accepted_currencies;
COMMIT;
