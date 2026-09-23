-- Ledger Entry: immutable record of one debit or credit on a Wallet with
-- the balance before and after. Append-only: triggers reject UPDATE, DELETE
-- and TRUNCATE for every role, the owner included, and the app role only
-- has INSERT and SELECT on it (000006_app_role).
CREATE TABLE ledger_entries (
    id                    uuid        PRIMARY KEY,
    wallet_id             uuid        NOT NULL REFERENCES wallets (id),
    transaction_id        uuid        NOT NULL REFERENCES wager_transactions (id),
    direction             text        NOT NULL,
    amount_minor          bigint      NOT NULL,
    currency              char(3)     NOT NULL,
    balance_before_minor  bigint      NOT NULL,
    balance_after_minor   bigint      NOT NULL,
    created_at            timestamptz NOT NULL,
    CONSTRAINT ledger_entries_direction_check CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_entries_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT ledger_entries_balances_non_negative
        CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT ledger_entries_balance_arithmetic CHECK (
        balance_after_minor = balance_before_minor
            + CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END
    ),
    CONSTRAINT ledger_entries_wallet_transaction_key UNIQUE (wallet_id, transaction_id)
);

-- Keyset pagination and Reconciliation scans in (created_at, id) order.
CREATE INDEX ledger_entries_wallet_created_id ON ledger_entries (wallet_id, created_at, id);

CREATE FUNCTION ledger_entries_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only: % is not allowed', TG_OP
        USING HINT = 'record a new compensating entry instead';
END;
$$;

CREATE TRIGGER ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_reject_mutation();

CREATE TRIGGER ledger_entries_no_truncate
    BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_entries_reject_mutation();
