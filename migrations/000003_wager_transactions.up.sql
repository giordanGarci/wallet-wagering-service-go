-- Wager Transaction: one financial operation on a Wallet, either external
-- (sent by a Provider) or internal (the Opening). Columns mirror
-- wager.Snapshot; optional values are NULL when absent.
--
-- wallet_id has no foreign key on purpose: a Provider operation naming an
-- unknown Wallet is persisted as REJECTED WALLET_NOT_FOUND so that its
-- Replay returns the same result.
CREATE TABLE wager_transactions (
    id                                 uuid        PRIMARY KEY,
    origin                             text        NOT NULL,
    kind                               text        NOT NULL,
    status                             text        NOT NULL,
    wallet_id                          uuid        NOT NULL,
    player_id                          uuid        NOT NULL,
    amount_minor                       bigint      NOT NULL,
    currency                           char(3)     NOT NULL,
    -- Balance returned to the Provider and replayed verbatim; its currency
    -- is the Wallet's, which differs from currency on CURRENCY_MISMATCH.
    balance_after_minor                bigint,
    balance_after_currency             char(3),
    failure_code                       text,

    -- External metadata (Provider operations only).
    provider_id                        text,
    external_transaction_id            text,
    idempotency_key                    text,
    payload_hash                       text,
    round_id                           text,
    game_id                            text,
    reference_external_transaction_id  text,

    -- Resolved Reference and consumed Compensation Slot (ADR-0002).
    reference_transaction_id           uuid,
    compensated_bet_id                 uuid,

    -- Pending Reference worker: retries done, next retry and claim lease.
    attempts                           integer     NOT NULL DEFAULT 0,
    next_attempt_at                    timestamptz,
    locked_until                       timestamptz,

    created_at                         timestamptz NOT NULL,
    updated_at                         timestamptz NOT NULL,

    CONSTRAINT wager_transactions_origin_check CHECK (origin IN ('EXTERNAL', 'INTERNAL')),
    CONSTRAINT wager_transactions_kind_check
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_transactions_status_check
        CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_transactions_amount_non_negative CHECK (amount_minor >= 0),
    CONSTRAINT wager_transactions_balance_after_non_negative CHECK (balance_after_minor >= 0),
    CONSTRAINT wager_transactions_balance_after_pair
        CHECK ((balance_after_minor IS NULL) = (balance_after_currency IS NULL)),
    CONSTRAINT wager_transactions_processed_balance
        CHECK (status <> 'PROCESSED' OR balance_after_minor IS NOT NULL),
    CONSTRAINT wager_transactions_failure_code_status
        CHECK ((failure_code IS NOT NULL) = (status IN ('REJECTED', 'FAILED'))),
    CONSTRAINT wager_transactions_attempts_non_negative CHECK (attempts >= 0),
    -- External operations carry their metadata; the internal Opening
    -- carries none (nor References, slots or retries) and is positive.
    CONSTRAINT wager_transactions_origin_metadata CHECK (
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
        OR
        (origin = 'INTERNAL' AND kind = 'OPENING' AND amount_minor > 0 AND attempts = 0
            AND num_nonnulls(provider_id, external_transaction_id, idempotency_key, payload_hash,
                             round_id, game_id, reference_external_transaction_id,
                             reference_transaction_id, compensated_bet_id,
                             next_attempt_at, locked_until) = 0)
    ),
    CONSTRAINT wager_transactions_payload_hash_hex CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT wager_transactions_reference_pair
        CHECK (reference_transaction_id IS NULL OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_transactions_pending_reference
        CHECK (status <> 'PENDING_REFERENCE' OR reference_external_transaction_id IS NOT NULL),
    CONSTRAINT wager_transactions_compensated_bet_reversal
        CHECK (compensated_bet_id IS NULL OR (kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED')),

    -- Idempotency: one operation per (provider, key) and per (provider,
    -- external id). The second also serves Reference lookups.
    CONSTRAINT wager_transactions_provider_idempotency_key UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wager_transactions_provider_external_transaction_id UNIQUE (provider_id, external_transaction_id)
);

-- At most one Opening per Wallet.
CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

-- Compensation Slot (ADR-0002): one successful Reversal per BET.
CREATE UNIQUE INDEX wager_transactions_compensated_bet_key
    ON wager_transactions (compensated_bet_id) WHERE compensated_bet_id IS NOT NULL;

-- A Reference is undone by at most one PROCESSED Reversal (for example a
-- single ROLLBACK of a given REFUND).
CREATE UNIQUE INDEX wager_transactions_processed_reversal_reference_key
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

-- Pending Reference worker claims: due retries first.
CREATE INDEX wager_transactions_pending_reference_due
    ON wager_transactions (next_attempt_at) WHERE status = 'PENDING_REFERENCE';
