-- Least-privilege application role. Migrations run as the schema owner; the
-- service connects as wallet_app, which reads and writes what it needs but
-- cannot rewrite history: ledger_entries is INSERT and SELECT only, and no
-- table grants DELETE, TRUNCATE or DDL.
--
-- Roles are cluster-wide while this schema is per database (the test
-- harness creates one database per test), so creation tolerates an existing
-- role, including one created concurrently. The password is a local
-- development value; production provisions the role and its secret outside
-- the migrations.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
        BEGIN
            CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            NULL; -- created by a concurrent migration
        END;
    END IF;
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO wallet_app', current_database());
END
$$;

GRANT USAGE ON SCHEMA public TO wallet_app;
GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, outbox_events, inbox_messages TO wallet_app;
GRANT SELECT, INSERT ON ledger_entries TO wallet_app;
