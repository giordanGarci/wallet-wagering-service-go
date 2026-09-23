-- Baseline: marks the schema as managed by the wallet service migrations.
-- Domain tables arrive in later migrations.
COMMENT ON SCHEMA public IS 'Wallet & Wagering service schema (managed by migrations/)';
