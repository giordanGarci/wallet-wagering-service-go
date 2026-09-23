REVOKE ALL ON wallets, wager_transactions, ledger_entries, outbox_events, inbox_messages FROM wallet_app;
REVOKE USAGE ON SCHEMA public FROM wallet_app;

-- The role is cluster-wide: drop it only when no other database still
-- grants it something.
DO $$
BEGIN
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM wallet_app', current_database());
    BEGIN
        DROP ROLE wallet_app;
    EXCEPTION WHEN dependent_objects_still_exist THEN
        RAISE NOTICE 'wallet_app keeps privileges in other databases; not dropped';
    END;
END
$$;
