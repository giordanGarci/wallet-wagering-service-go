-- Outbox Event: written in the same commit as the change it reports and
-- published later by the outbox worker. payload holds the exact JSON bytes
-- of the envelope (event.Event.MarshalJSON), so republication is byte for
-- byte identical; text keeps it readable and IS JSON keeps it valid.
CREATE TABLE outbox_events (
    event_id         uuid        PRIMARY KEY,
    -- Insertion order: the publisher sends a Wallet's events in this order
    -- (MessageGroupId = aggregate_id).
    seq              bigint      GENERATED ALWAYS AS IDENTITY,
    aggregate_id     uuid        NOT NULL,
    event_type       text        NOT NULL,
    version          integer     NOT NULL,
    correlation_id   text        NOT NULL,
    payload          text        NOT NULL,
    occurred_at      timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- Publication state: failed attempts, next try, claim lease, outcome.
    attempts         integer     NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    locked_until     timestamptz,
    published_at     timestamptz,
    last_error       text,
    CONSTRAINT outbox_events_seq_key UNIQUE (seq),
    CONSTRAINT outbox_events_version_positive CHECK (version >= 1),
    CONSTRAINT outbox_events_attempts_non_negative CHECK (attempts >= 0),
    CONSTRAINT outbox_events_payload_json CHECK (payload IS JSON OBJECT)
);

-- Publisher claims: unpublished events that are due, oldest first.
CREATE INDEX outbox_events_unpublished_due
    ON outbox_events (next_attempt_at, seq) WHERE published_at IS NULL;

-- Inbox Message: one per (consumer, SQS messageId), written in the same SQL
-- transaction as the operation it carried.
CREATE TABLE inbox_messages (
    consumer_name   text        NOT NULL,
    message_id      text        NOT NULL,
    body_hash       text        NOT NULL,
    received_at     timestamptz NOT NULL,
    completed_at    timestamptz,
    transaction_id  uuid        REFERENCES wager_transactions (id),
    CONSTRAINT inbox_messages_consumer_message_key PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_messages_body_hash_hex CHECK (body_hash ~ '^[0-9a-f]{64}$')
);
