# Wallet & Wagering Service

Go service that moves player wallets based on game provider operations (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), received over HTTP and SQS, with an append-only ledger, persistent idempotency, inbox/outbox and Keycloak authentication.

## Prerequisites

| Tool | Version | Use |
| --- | --- | --- |
| Docker + Docker Compose v2 | Recent Docker Desktop | Local infrastructure and integration tests |
| Go | declared in `go.mod` | Build and tests outside the container (`GOTOOLCHAIN=auto` downloads the right version) |
| curl + jq | any | Authenticated call examples |

## Local environment services

| Service | Port | Description |
| --- | --- | --- |
| `app` | 8080 | HTTP API, SQS consumer, outbox publisher and pending references worker |
| `postgres` | 5432 | PostgreSQL |
| `keycloak` | 8081 | OAuth 2.0/OIDC IdP with the `wallet` realm provisioned automatically (`deploy/keycloak/wallet-realm.json`); host port adjustable with `KEYCLOAK_PORT` |
| `ministack` | 4566 | AWS emulator (SQS, image `ministackorg/ministack`), no account or token needed |
| `sqs-init` | | Creates the queues and redrive policy, then exits (`deploy/sqs-init/init.sh`) |
| `migrate` | | Applies the migrations (`golang-migrate` CLI), then exits |

## Environment variables

Copy the example and adjust if needed:

```sh
cp .env.example .env
```

| Variable | Example | Default | Description |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | `:8080` | HTTP server address |
| `DATABASE_URL` | `postgres://wallet_app:wallet_app@postgres:5432/wallet?sslmode=disable` | required | Application PostgreSQL connection, using the least-privilege role `wallet_app` |
| `MIGRATE_DATABASE_URL` | `postgres://wallet:wallet@postgres:5432/wallet?sslmode=disable` | | Schema owner connection, used only by the `migrate` service |
| `AWS_ENDPOINT_URL` | `http://ministack:4566` | real AWS endpoint | SQS emulator endpoint |
| `AWS_REGION` | `us-east-1` | `us-east-1` | AWS region |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `wallet-consumer` / `local-secret` | SDK default chain | Consumer credentials on the broker |
| `SQS_INPUT_QUEUE_URL` | `http://ministack:4566/000000000000/wager-transactions.fifo` | required | Input queue |
| `SQS_EVENTS_QUEUE_URL` | `http://ministack:4566/000000000000/wallet-events.fifo` | required | Queue for events published by the outbox |
| `SQS_DLQ_QUEUE_URL` | `http://ministack:4566/000000000000/wager-transactions-dlq.fifo` | required with the `consumer` role | Input DLQ, where the consumer sends permanent errors |
| `CONSUMER_NAME` | `wager-transactions` | `wager-transactions` | `consumer_name` of the Inbox Messages; instances consuming the same queue must use the same value |
| `CONSUMER_CONCURRENCY` | `10` | `10` | Messages handled at the same time (1 to 100); messages with the same `MessageGroupId` are always sequential |
| `CONSUMER_WAIT_TIME` | `20s` | `20s` | `ReceiveMessage` long polling (whole seconds, 0s to 20s) |
| `CONSUMER_VISIBILITY_TIMEOUT` | `30s` | `30s` | Visibility requested on each receive and renewed every 1/3 of it during processing |
| `CONSUMER_RETRY_BASE` / `CONSUMER_RETRY_MAX` | `1s` / `2m` | `1s` / `2m` | Exponential backoff with jitter (by `ApproximateReceiveCount`) applied with `ChangeMessageVisibility` after a transient failure |
| `OIDC_ISSUER_URL` | `http://localhost:8081/realms/wallet` | required with the `api` role | Expected issuer, identical to the tokens' `iss` claim (in compose, derived from `KEYCLOAK_PORT`) |
| `OIDC_AUDIENCE` | `wallet-api` | `wallet-api` | Audience required in the `aud` claim |
| `OIDC_JWKS_URL` | `http://keycloak:8080/realms/wallet/protocol/openid-connect/certs` | `<OIDC_ISSUER_URL>/protocol/openid-connect/certs` | Where to fetch the signing keys, when the issuer is unreachable by its public name (e.g. inside the compose network) |
| `APP_ROLES` | `api,consumer,outbox,pending` | all | Components active in this instance (comma-separated list) |
| `PENDING_MAX_ATTEMPTS` | `8` | `8` | Pending References worker attempts (1 to 1000) before rejecting with `REFERENCE_NOT_FOUND` |
| `PENDING_RETRY_BASE` / `PENDING_RETRY_MAX` | `2s` / `2m` | `2s` / `2m` | Exponential backoff (no jitter) between attempts of a Pending Reference: base, 2x base, ... up to the cap (base cannot exceed the cap). Applies to every role: whoever parks the operation schedules the first attempt |
| `PENDING_POLL_INTERVAL` | `1s` | `1s` | Pending References worker pause when no pending items are due |
| `PENDING_BATCH_SIZE` | `20` | `20` | Pending items claimed per cycle (1 to 1000) |
| `PENDING_LEASE_DURATION` | `30s` | `30s` | Batch lease (`locked_until`); after it, another instance resumes the unfinished pending items |
| `OUTBOX_POLL_INTERVAL` | `500ms` | `500ms` | Outbox publisher pause when no events are due |
| `OUTBOX_BATCH_SIZE` | `50` | `50` | Events claimed per cycle (1 to 1000) |
| `OUTBOX_LEASE_DURATION` | `30s` | `30s` | Batch lease (`locked_until`); after it, another publisher resumes the unconfirmed events |
| `OUTBOX_RETRY_BASE` / `OUTBOX_RETRY_MAX` | `1s` / `5m` | `1s` / `5m` | Exponential backoff with jitter after a publish failure (base cannot exceed the cap) |
| `SHUTDOWN_TIMEOUT` | `25s` | `25s` | Deadline to finish in-flight work on `SIGTERM` |
| `LOG_LEVEL` | `info` | `info` | JSON log level: `debug`, `info`, `warn`, `error` |

`.env.example` contains only local example values, with no real secrets. Compose loads `.env.example` and, if present, `.env` on top of it. Configuration is validated before any connection: an invalid value exits the process with status 2 and a single message listing every problem.

All roles (`api`, `consumer`, `outbox`, `pending`) are implemented. The HTTP server (health and `/metrics`) runs on every instance, whatever `APP_ROLES` is.

## Running

```sh
docker compose up --build
```

Startup order: `postgres`, `keycloak` and `ministack` healthy, then `migrate` and `sqs-init`, then `app`. Check:

```sh
curl -s localhost:8080/health/live    # {"status":"ok"}
curl -s localhost:8080/health/ready   # {"status":"ok","checks":{"postgres":"up","sqs":"up"}}
curl -s localhost:8080/metrics        # Prometheus format
```

`/health/ready` responds `503` with `"status":"unavailable"` and the affected check as `"down"` when PostgreSQL or any configured queue fails to respond within 2s. At startup, the application fails fast if PostgreSQL or the queues are unreachable. On `SIGTERM`, the HTTP server stops accepting connections first, the SQS consumer stops fetching messages, waits for in-flight ones and resets the visibility of those it cannot finish shortly before the deadline, the outbox publisher and the Pending References worker finish in-flight work and release the leases of the rest, then the pgx pool and the SQS client are closed, all within `SHUTDOWN_TIMEOUT`.

MiniStack keeps no state: if the `ministack` container restarts, recreate the queues with `docker compose run --rm sqs-init`.

Multiple instances: any instance can take any role, and the guarantees hold with several running at once. The compose `app` service publishes the fixed port 8080, so `--scale` does not apply; the `e2e` suite (section [Tests](#tests)) starts three independent processes of the binary against the same infrastructure.

## SQS queues

The `sqs-init` container provisions automatically:

| Queue | Configuration |
| --- | --- |
| `wager-transactions.fifo` | Input; `VisibilityTimeout=30`, `ReceiveMessageWaitTimeSeconds=20`, redrive to the DLQ with `maxReceiveCount=5` |
| `wager-transactions-dlq.fifo` | Input dead-letter queue |
| `wallet-events.fifo` | Destination of outbox events; `MessageGroupId=aggregateId`, `MessageDeduplicationId=eventId` |

To recreate manually:

```sh
docker compose run --rm sqs-init
```

The `outbox` role publishes Outbox Events after the commit that produced them. Each cycle claims up to `OUTBOX_BATCH_SIZE` due events with `FOR UPDATE SKIP LOCKED` and a lease of `OUTBOX_LEASE_DURATION`, sends the stored payload byte for byte (`MessageGroupId=aggregateId`, `MessageDeduplicationId=eventId`) and only then sets `published_at`. Several publishers share the outbox without sending the same record in parallel; an expired lease (a publisher that crashed) is resumed by another one, with the same `eventId`. A Wallet's events go out in the order they were stored: an event is only claimed together with all earlier unpublished events of the same Wallet, and a failure holds back the following ones. A send failure increments `attempts`, stores `last_error` and schedules `next_attempt_at` with exponential backoff and jitter. Metrics: `wallet_outbox_lag_seconds` (age of the oldest unpublished event), `wallet_outbox_published_total`, `wallet_outbox_publish_failures_total` and `wallet_outbox_retries_total`.

To read the published events:

```sh
docker compose exec ministack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wallet-events.fifo \
  --attribute-names All --max-number-of-messages 10
```

Input messages use `MessageGroupId = walletId` and `MessageDeduplicationId = messageId`. Details in [ARCHITECTURE.md](ARCHITECTURE.md).

#### `wager-transactions.fifo` consumer (`consumer` role)

Accepted envelope (strict decoding: unknown fields are rejected, as in the HTTP body):

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "version": 1,
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "correlationId": "optional",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" },
    "referenceExternalTransactionId": "optional (REFUND, ROLLBACK, WIN)"
  }
}
```

- `messageId`, `type`, `occurredAt` (RFC 3339) and `data` are required; `version` is optional and, if present, must be `1`; without a `correlationId` in the envelope, the consumer uses the `correlationId` message attribute or generates a UUIDv7.
- `data` goes through the same `wager.NewCommand` as HTTP: same format rules, same Payload Hash and same use case (`ProcessWagerTransaction`). `data.idempotencyKey` is the Idempotency Key. An operation sent over HTTP and then over SQS (or the reverse) with the same key and the same payload is a Replay, with no new movement.
- Trust model: the `providerId` comes from the message. There is no per-message token; who can publish to the queue is controlled by the broker's credentials and IAM policies (the consumer uses its own credentials, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`), so each Provider should get `SendMessage` permission only on its own queue or through a trusted producer.
- Inbox Message: `UNIQUE (consumer_name, message_id)` with the SHA-256 of the raw body, stored in the same SQL transaction as the operation, the ledger and the outbox (also on Replays and on `PENDING_REFERENCE`, which completes the Inbox Message and leaves continuation to the pending worker). A redelivery with the same `messageId` and the same hash is removed with no effect; with a different hash it goes to the DLQ.
- The message is only deleted after the commit. `PROCESSED`, `REJECTED` (including `WALLET_NOT_FOUND`), `PENDING_REFERENCE` and `FAILED` are terminal results; the first three delete the message and `FAILED` sends it to the DLQ (`permanent_error`) after it is recorded.
- Permanent errors are sent explicitly to the DLQ (same body and `MessageGroupId`, `MessageDeduplicationId` = SQS `MessageId`, attributes `dlqReason`, `dlqDetail` and `dlqSourceMessageId`) and deleted from the input. Reasons: `malformed_json`, `unknown_envelope` (unknown type or version), `invalid_envelope`, `invalid_command` (format validation, equivalent to 400), `opening_kind`, `body_hash_mismatch`, `idempotency_key_conflict` and `external_transaction_conflict` (equivalent to 409: resending the same message never resolves it) and `permanent_error` (operation recorded as `FAILED PERMANENT_ERROR`, equivalent to 500).
- Transient errors (PostgreSQL unavailable, timeout, concurrency conflict) apply `ChangeMessageVisibility` with exponential backoff and jitter by `ApproximateReceiveCount` (`CONSUMER_RETRY_BASE` to `CONSUMER_RETRY_MAX`); after 5 receives the queue's redrive policy moves the message to the DLQ (without `dlqReason`).
- Concurrency: up to `CONSUMER_CONCURRENCY` messages per instance; messages of the same group (same Wallet) are handled one at a time, in order. If one does not complete, the following ones of the same group in the batch are released (visibility 0) and come back after it.
- During processing, visibility is renewed every `CONSUMER_VISIBILITY_TIMEOUT/3`. On `SIGTERM`, polling stops, in-flight messages may complete until shortly before `SHUTDOWN_TIMEOUT` (one fifth of the deadline, at most 3s, is reserved) and the remaining ones are cancelled (rollback) and released with visibility 0 for another instance.
- Metrics: `wallet_sqs_messages_received_total`, `wallet_sqs_messages_handled_total{result}` (`processed`, `rejected`, `pending_reference`, `failed`, `replay`, `duplicate`, `dead_lettered`, `retried`, `released`), `wallet_sqs_dlq_messages_total{reason}`, `wallet_inbox_duplicates_total`, `wallet_sqs_retries_total` and `wallet_sqs_visibility_extensions_total`. Logs include `messageId`, `sqsMessageId`, `correlationId`, `transactionId`, `walletId` and `providerId`, without the payload.

To read the DLQ:

```sh
docker compose exec ministack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions-dlq.fifo \
  --attribute-names All --message-attribute-names All --max-number-of-messages 10
```

## Migrations

Migrations are versioned in `migrations/` in the [golang-migrate](https://github.com/golang-migrate/migrate) format (`NNNNNN_description.up.sql` and `NNNNNN_description.down.sql`, sequential 6-digit numbering) and applied by the `migrate` service when compose starts, as the schema owner (`MIGRATE_DATABASE_URL`). Integration tests apply the same files, embedded in the `migrations` package.

| Table | Contents |
| --- | --- |
| `wallets` | Balance (`BIGINT` in minor units + `CHAR(3)`) and version; `UNIQUE (player_id, currency)`, `CHECK (balance >= 0)` |
| `wager_transactions` | Wager Transactions (mirrors `wager.Snapshot`); origin `CHECK` that requires external metadata only for `EXTERNAL` and forbids it on the Opening; one Opening per Wallet; `UNIQUE (provider_id, idempotency_key)` and `UNIQUE (provider_id, external_transaction_id)`; Compensation Slot indexes |
| `ledger_entries` | Append-only ledger: before/after balance `CHECK`, `UNIQUE (wallet_id, transaction_id)`, trigger that rejects `UPDATE`, `DELETE` and `TRUNCATE` even for the owner |
| `outbox_events` | Exact JSON envelope of each Outbox Event, with attempts, `next_attempt_at`, `locked_until` and `published_at` |
| `inbox_messages` | Received SQS messages, `UNIQUE (consumer_name, message_id)` |

Migration `000006_app_role` creates the `wallet_app` role (local password `wallet_app`), with `SELECT`, `INSERT` and `UPDATE` on the tables and only `INSERT` and `SELECT` on `ledger_entries`; no table grants `DELETE`, `TRUNCATE` or DDL. The application connects with this role. In production, the role and password are provisioned outside the migrations.

```sh
# apply all
docker compose run --rm migrate up

# revert the last one
docker compose run --rm migrate down 1

# current version
docker compose run --rm migrate version
```

## Authentication

Keycloak imports the `wallet` realm automatically on startup. All clients use `client_credentials`.

| Client | Local secret | Permissions |
| --- | --- | --- |
| `provider-a` | `provider-a-secret` | Sends and queries only `provider-a` transactions |
| `provider-b` | `provider-b-secret` | Sends and queries only `provider-b` transactions |
| `wallet-internal` | `wallet-internal-secret` | Wallet opening, reading, ledger and reconciliation |

All three clients receive `wallet-api` in the `aud` claim (audience mapper); Provider clients also receive the fixed `provider_id` claim (`provider-a` or `provider-b`). The authorized `providerId` comes from this claim. A body with a `providerId` different from the token gets `403`; another provider's transaction responds `404`.

The application validates tokens locally, without calling Keycloak on every request: signature against the cached JWKS (fetched at startup, refreshed every hour and when an unknown `kid` shows up, at most every 30s), `iss` equal to `OIDC_ISSUER_URL`, `aud` containing `OIDC_AUDIENCE`, required `exp` and `nbf`, with 30s of clock skew tolerance. If the JWKS is unreachable at startup, the application does not start.

| Situation | Response |
| --- | --- |
| Missing `Authorization: Bearer`, malformed token, invalid signature, expired, not yet valid, from another issuer or without the audience | `401 UNAUTHENTICATED` with `WWW-Authenticate: Bearer` |
| Provider token on an internal endpoint (`/wallets...`) | `403 FORBIDDEN` |
| `wallet-internal` token on a Provider endpoint (`/wagering/...`, `/providers/...`) | `403 FORBIDDEN` |
| Valid token from a client with no role in the service | `403 FORBIDDEN` |

**Single issuer across host and containers.** Keycloak runs with `KC_HOSTNAME=http://localhost:${KEYCLOAK_PORT:-8081}`, so every token carries `iss=http://localhost:8081/realms/wallet`, whether obtained from the host (`localhost:8081`) or from inside the compose network (`keycloak:8080`). Inside compose that address does not reach Keycloak, so `app` fetches the JWKS through `OIDC_JWKS_URL=http://keycloak:8080/...`. If port 8081 is taken on the host, start with `KEYCLOAK_PORT=8082 docker compose up`: the published port, the Keycloak issuer and the `app`'s `OIDC_ISSUER_URL` change together (use the same port in the commands below).

Every API error uses a single body, with a stable `code` and a readable `message`:

```json
{"code": "WALLET_ALREADY_EXISTS", "message": "a wallet already exists for this player and currency"}
```

Current codes: `INVALID_REQUEST` (400), `UNAUTHENTICATED` (401), `FORBIDDEN` (403), `NOT_FOUND` (404), `WALLET_ALREADY_EXISTS`, `IDEMPOTENCY_KEY_CONFLICT` and `EXTERNAL_TRANSACTION_CONFLICT` (409), `INTERNAL_ERROR` (500), `SERVICE_UNAVAILABLE` (503, with `Retry-After`).

Getting a token:

```sh
token() {
  curl -s -X POST localhost:8081/realms/wallet/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-secret" \
    | jq -r .access_token
}
INTERNAL=$(token wallet-internal)
PROVIDER_A=$(token provider-a)
```

## Call examples

Open a wallet (`wallet-internal` only):

```sh
curl -s -X POST localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -H 'X-Correlation-Id: demo-123' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
# 201 {"id":"<UUIDv7>","playerId":"0192f28f-...","balance":{"amount":"1000.00","currency":"BRL"},"version":1,"createdAt":"..."}
```

The body is decoded strictly (unknown fields, trailing JSON, a numeric value or one outside the canonical `0.00` form get `400 INVALID_REQUEST`). A second opening for the same (player, currency) pair gets `409 WALLET_ALREADY_EXISTS`; simultaneous openings of the same pair result in a single Wallet.

A positive initial balance stores, in the same commit, the Wallet (version 1), a `PROCESSED` Opening, its credit Ledger Entry and the `WagerTransactionProcessed` and `WalletBalanceChanged` Outbox Events. An initial balance of `0.00` creates only the Wallet, with no Opening, ledger or events.

Every response carries `X-Correlation-Id`: the value sent by the client (up to 128 characters of `[A-Za-z0-9._:-]`) or, if missing or invalid, a generated UUIDv7. The same id shows up as `correlationId` in the request logs and in the Outbox Events it produces.

Fetch the wallet: `GET /wallets/<walletId>` responds `200` with the same body, `404 NOT_FOUND` for an unknown id and `400 INVALID_REQUEST` for an id that is not a UUID.

List the ledger (`wallet-internal` only), in stable order by `(createdAt, id)`:

```sh
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
# 200 {"items":[{"id":"<UUIDv7>","transactionId":"<Opening id>","direction":"CREDIT",
#       "money":{"amount":"1000.00","currency":"BRL"},"balanceBefore":{"amount":"0.00","currency":"BRL"},
#       "balanceAfter":{"amount":"1000.00","currency":"BRL"},"createdAt":"..."}],"nextCursor":null}
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=50&cursor=<nextCursor>" -H "Authorization: Bearer $INTERNAL"
```

`limit` ranges from 1 to 200 (default 50). `nextCursor` is opaque (base64url of the last item's position) and `null` on the last page; new entries appear at the end without repeating those already read. A `limit` out of range or an invalid cursor gets `400 INVALID_REQUEST`; a nonexistent wallet, `404 NOT_FOUND`.

Reconcile the wallet (`wallet-internal` only): compares the stored balance with credits minus debits of all Ledger Entries, Opening included.

```sh
curl -s -X POST localhost:8080/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL"
# 200 {"walletId":"<walletId>","storedBalance":{"amount":"1000.00","currency":"BRL"},
#      "calculatedBalance":{"amount":"1000.00","currency":"BRL"},"difference":{"amount":"0.00","currency":"BRL"},
#      "consistent":true,"checkedEntries":1}
```

The read runs in a `REPEATABLE READ READ ONLY` transaction (balance and aggregate from the same snapshot) and never changes the balance. `difference` is stored minus calculated and can be negative. A divergence still responds `200` with `"consistent":false`, emits an `ERROR` log `reconciliation divergence` with `walletId`, values and `correlationId`, and increments `wallet_reconciliation_divergences_total`; every run counts in `wallet_reconciliations_total{result="consistent"|"divergent"}` (`GET /metrics`). A Provider token gets `403`, a nonexistent wallet `404`.

Place a bet (use the returned `id` as `walletId`):

```sh
curl -s -X POST localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123",
       "playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"<walletId>",
       "roundId":"round-987","gameId":"fortune-chimp","kind":"BET",
       "money":{"amount":"25.00","currency":"BRL"}}'
# 200 {"transactionId":"<UUIDv7>","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}
```

The operation is validated, applied and completed in a single SQL transaction (`SELECT ... FOR UPDATE` on the Wallet, `UPDATE` with version check, Wager Transaction, Ledger Entry and Outbox Events in the same commit). The same use case (`app.ProcessWagerTransaction`) serves HTTP and SQS.

| Result | HTTP | Body |
| --- | --- | --- |
| `PROCESSED` | `200` | `transactionId`, `status`, `balance` (balance after the operation), `idempotentReplay` |
| `REJECTED` | `422` | same, with `failureCode` (`INSUFFICIENT_FUNDS`, `WALLET_NOT_FOUND`, `WALLET_PLAYER_MISMATCH`, `CURRENCY_MISMATCH`, ...); `balance` is the balance observed at rejection, or `null` for `WALLET_NOT_FOUND` and `WALLET_PLAYER_MISMATCH` |
| `PENDING_REFERENCE` | `202` | same, `balance: null` |
| `FAILED` | `500` | same, `failureCode: "PERMANENT_ERROR"`, `balance: null` (permanent failure recorded for auditing) |
| Replay (same key, same content) | status of the original result | persisted result, with the balance at that time and `idempotentReplay: true` |
| Missing `Idempotency-Key`, invalid body, Money outside the canonical form, zero amount, `OPENING`, missing or unknown field | `400 INVALID_REQUEST` | nothing is recorded |
| Body `providerId` different from the token | `403 FORBIDDEN` | nothing is recorded |
| Same key with different content / same `externalTransactionId` with a different key | `409 IDEMPOTENCY_KEY_CONFLICT` / `409 EXTERNAL_TRANSACTION_CONFLICT` | nothing is recorded |
| Transient PostgreSQL failure | `503 SERVICE_UNAVAILABLE` with `Retry-After` | nothing is recorded; resending is safe |

Fetch an operation (only the Provider's own; another provider's responds `404`, and a route `providerId` different from the token responds `403`):

```sh
curl -s localhost:8080/wagering/transactions/<transactionId> -H "Authorization: Bearer $PROVIDER_A"
curl -s localhost:8080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER_A"
# 200 {"transactionId":"...","providerId":"provider-a","externalTransactionId":"transaction-123","kind":"BET",
#      "status":"PROCESSED","walletId":"...","playerId":"...","roundId":"round-987","gameId":"fortune-chimp",
#      "money":{"amount":"25.00","currency":"BRL"},"balance":{"amount":"975.00","currency":"BRL"},
#      "createdAt":"...","updatedAt":"..."}
```

Undo the bet with a Reversal (`REFUND` or `ROLLBACK`): same player, Wallet, currency, Round and amount as the Reference, with `referenceExternalTransactionId` required (missing: `400`):

```sh
curl -s -X POST localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:refund-123' \
  -d '{"providerId":"provider-a","externalTransactionId":"refund-123",
       "playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"<walletId>",
       "roundId":"round-987","gameId":"fortune-chimp","kind":"REFUND",
       "money":{"amount":"25.00","currency":"BRL"},
       "referenceExternalTransactionId":"transaction-123"}'
# 200 {"transactionId":"<UUIDv7>","status":"PROCESSED","balance":{"amount":"1000.00","currency":"BRL"},"idempotentReplay":false}
# a second REFUND or ROLLBACK of the same BET:
# 422 {"transactionId":"...","status":"REJECTED","balance":{"amount":"1000.00","currency":"BRL"},
#      "failureCode":"REFERENCE_ALREADY_COMPENSATED","idempotentReplay":false}
```

Rules (Compensation Slot, [ADR-0002](docs/adr/0002-compensation-slot.md)):

| Reversal | Effect | Rejections |
| --- | --- | --- |
| `REFUND` or `ROLLBACK` of `BET` | credits the BET amount; consumes the BET's slot (the first one wins) | another Reversal of the same BET: `REFERENCE_ALREADY_COMPENSATED` |
| `ROLLBACK` of `WIN` | debits the WIN amount (once) | insufficient balance: `REVERSAL_INSUFFICIENT_FUNDS` (distinct from the BET's `INSUFFICIENT_FUNDS`) |
| `ROLLBACK` of `REFUND` | debits it back; the BET's slot stays consumed | new `REFUND`/`ROLLBACK` of the BET: `REFERENCE_ALREADY_COMPENSATED` |
| `ROLLBACK` of `ROLLBACK`, `REFUND` of `WIN`/`REFUND`/`ROLLBACK`, Reversal of `LOSS` | none | `REFERENCE_NOT_REVERSIBLE` |

`REJECTED` or `FAILED` Reference: `REFERENCE_UNSUCCESSFUL`. Different player, Wallet, currency or Round: `REFERENCE_MISMATCH`; different amount: `REFERENCE_AMOUNT_MISMATCH` (both correctable; nothing is consumed). The Reference is looked up only among the operations of the token's `providerId`: another provider's BET is, for the sender, a missing Reference, and the Reversal stays in `PENDING_REFERENCE` (`202`) without moving the balance. Concurrent Reversals of the same BET are serialized by the Wallet lock (exactly one `PROCESSED`); the partial unique indexes `wager_transactions_compensated_bet_key` and `wager_transactions_processed_reversal_reference_key` are the final guarantee.

### Pending References

An operation whose Reference has not arrived yet (or is itself in `PENDING_REFERENCE`) is committed as `PENDING_REFERENCE` (`202`, `balance: null`, event `WagerTransactionPendingReference`) without moving the balance, with the first attempt scheduled in `next_attempt_at`. This applies to REFUND, ROLLBACK and WIN with a Reference. The `pending` role resumes these operations: each cycle claims up to `PENDING_BATCH_SIZE` due pending items with `FOR UPDATE SKIP LOCKED` and a lease of `PENDING_LEASE_DURATION`; each attempt runs in a SQL transaction (`app.ResumePendingReference`) that locks the Wallet, rereads the pending item under the lease, looks up the Reference in the same Provider and applies the rules above:

| Reference | Result |
| --- | --- |
| `PROCESSED` | the operation is applied (balance, Ledger Entry, `WagerTransactionProcessed` and `WalletBalanceChanged`), or rejected by the Reversal rules |
| `REJECTED` or `FAILED` | `REJECTED` with `REFERENCE_UNSUCCESSFUL` and `WagerTransactionRejected` |
| missing or `PENDING_REFERENCE` | `attempts + 1` and a new attempt with backoff (`PENDING_RETRY_BASE`, doubling up to `PENDING_RETRY_MAX`); on reaching `PENDING_MAX_ATTEMPTS`, `REJECTED` with `REFERENCE_NOT_FOUND` and `WagerTransactionRejected`, with no Ledger Entry |

Chains (ROLLBACK of a REFUND waiting for its BET) resolve in order: the ROLLBACK stays pending while the REFUND is pending. A Replay of a pending operation returns `202` with the current state; after resolution, it returns the persisted final result (`200` or `422`, `idempotentReplay: true`). With the default configuration, a pending item whose Reference never arrives is rejected about 6 minutes later; a Reference that arrives is applied on the next scheduled attempt (up to `PENDING_RETRY_MAX` later).

The state lives in the database, so pending items are resumed after a restart and by any instance with the `pending` role. An instance that dies holding the lease is replaced when the lease expires; every write is conditioned on the lease (`locked_until`) and on the status, and the locked Wallet serializes the attempt with any other operation on the Wallet, so two instances never apply the same pending item twice. On `SIGTERM`, the in-flight attempt finishes and claimed pending items not yet attempted have their lease released. A permanent (non-transient) error completes the pending item as `FAILED PERMANENT_ERROR`. Metrics: `wallet_pending_reference_retries_total`, `wallet_pending_reference_resolutions_total{outcome="processed|rejected|expired|failed"}` and `wallet_pending_reference_backlog`.

Metrics: `wallet_wager_transactions_total{kind,status}`, `wallet_wager_duplicates_total{outcome}`, `wallet_concurrency_conflicts_total` and `wallet_wager_processing_seconds{channel,result}` on `/metrics`.

Idempotency: the `Idempotency-Key` is stored exactly as sent (no case normalization or replacement with a computed key) and is unique per Provider, as is the `externalTransactionId`. The Payload Hash (SHA-256 of the canonical JSON of the business fields, lowercase UUIDs, excluding the key and transport metadata) decides between Replay and `409`. The Replay reads the persisted result: same status, same `failureCode` and the balance at that time, even after other movements or after restarting every instance; it writes no ledger entries or events. The same key sent by another Provider is a different operation. `wallet_wager_duplicates_total{outcome="replay"|"idempotency_key_conflict"|"external_transaction_conflict"}` counts duplicates.

WIN and LOSS use the same endpoint:

```sh
# WIN credits; referenceExternalTransactionId is optional and must point to a BET of the same Round,
# player, Wallet and currency (otherwise 422 REFERENCE_MISMATCH; rejected BET: 422 REFERENCE_UNSUCCESSFUL;
# BET not received yet: 202 PENDING_REFERENCE, resumed by the worker)
  -d '{..., "kind":"WIN", "money":{"amount":"50.00","currency":"BRL"}, "referenceExternalTransactionId":"transaction-123"}'
# 200 {"transactionId":"...","status":"PROCESSED","balance":{"amount":"1025.00","currency":"BRL"},"idempotentReplay":false}

# LOSS closes the Round without moving the Wallet: only "0.00" in the Wallet's currency, no Reference
  -d '{..., "kind":"LOSS", "money":{"amount":"0.00","currency":"BRL"}}'
# 200 {"transactionId":"...","status":"PROCESSED","balance":{"amount":"1025.00","currency":"BRL"},"idempotentReplay":false}
```

WIN produces a credit Ledger Entry, `WagerTransactionProcessed` and `WalletBalanceChanged`. LOSS produces only `WagerTransactionProcessed`: no Ledger Entry and no change to the Wallet version. WIN with a zero amount, LOSS with an amount other than `"0.00"` and LOSS with a Reference respond `400` with nothing recorded.

Queries:

```sh
curl -s localhost:8080/wallets/<walletId> -H "Authorization: Bearer $INTERNAL"
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
curl -s -X POST localhost:8080/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL"
curl -s localhost:8080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER_A"
```

Send through the queue (envelope described in [SQS queues](#wager-transactionsfifo-consumer-consumer-role)):

```sh
docker compose exec ministack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id <walletId> --message-deduplication-id msg-123 \
  --message-body '{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
    "data":{"providerId":"provider-a","externalTransactionId":"transaction-456","idempotencyKey":"provider-a:transaction-456",
    "playerId":"<playerId>","walletId":"<walletId>","roundId":"round-987","gameId":"fortune-chimp",
    "kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}'
curl -s localhost:8080/providers/provider-a/wagering/transactions/transaction-456 -H "Authorization: Bearer $PROVIDER_A"
```

HTTP status codes and response bodies (400, 401, 403, 404, 409, 422, 202, 503) are documented in [ARCHITECTURE.md](ARCHITECTURE.md).

## Tests

| Suite | Command | Requires |
| --- | --- | --- |
| Unit | `go test ./...` | Nothing |
| Unit with race | `go test -race ./...` | Nothing (CGO enabled) |
| Unit with race, no C compiler on the host | `scripts/test-race.sh` | Docker |
| Static analysis | `go vet ./...` | Nothing |
| Integration | `go test -tags=integration -race ./...` | Test infrastructure |
| Integration with race, no C compiler on the host | `scripts/test-race.sh -tags=integration ./...` | Test infrastructure |
| Multiple instances (3 processes) | `go test -tags=e2e -race ./test/e2e/...` | Test infrastructure |
| Multiple instances with race, no C compiler on the host | `scripts/test-race.sh -tags=e2e ./test/e2e/...` | Test infrastructure, Docker |
| Fault simulation | `go test -tags=e2e,faultinject -timeout 10m ./test/e2e/...` | Test infrastructure |

### Preparing the test infrastructure

```sh
docker compose up -d postgres keycloak ministack migrate sqs-init
```

Integration tests create an isolated database per run and queues with a random suffix, so they can run alongside the development environment. The harness (`internal/testsupport`) starts the Fx application inside the test process and reads the endpoints from these variables:

| Variable | Default | Use |
| --- | --- | --- |
| `TEST_DATABASE_URL` | `postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable` | Admin connection for `CREATE`/`DROP DATABASE` of the isolated database; the same user applies the migrations and runs the SQL assertions, while the application connects as `wallet_app` |
| `TEST_AWS_ENDPOINT_URL` | `http://localhost:4566` | SQS endpoint where the isolated queues are created |
| `TEST_KEYCLOAK_URL` | `http://localhost:8081` | Keycloak from which tests obtain real tokens (`testsupport.Token`) and the JWKS |
| `TEST_OIDC_ISSUER_URL` | `<TEST_KEYCLOAK_URL>/realms/wallet` | Issuer the application expects in tests (differs from the previous one only inside the compose network) |
| `TEST_LOG_LEVEL` | `warn` | Application log level during tests |

Authentication tests use real Keycloak tokens and, for cases Keycloak would never issue (expired, another audience, another issuer, invalid signature), `testsupport.Forger`: a test RSA key published in a local JWKS that merges Keycloak's keys with its own.

`scripts/test-race.sh` runs `go test -race` inside the `golang` image of the same version as `go.mod` (the race detector requires CGO); with the `integration` or `e2e` tag, the container joins the compose `wallet_default` network and points these variables to `postgres`, `ministack` and `keycloak`.

### Multiple instances

Tests with the `e2e` tag (`test/e2e`) build `cmd/app` once (with `-race` when the test runs with `-race`) and, in each scenario, create an isolated database and queues and start three independent processes, each with its own port, connection pool and memory, all with `APP_ROLES=api,consumer,outbox,pending` and short worker intervals. The scenarios:

| Scenario | Guarantee |
| --- | --- |
| Same BET 50 times in parallel, spread across the instances | One debit, 1 new response and 49 Replays of the same transaction |
| Two BETs of 80.00 against 100.00 on different instances | One PROCESSED, one `INSUFFICIENT_FUNDS`, balance 20.00, one debit; resends to each instance keep the result |
| 30 Wallets with 5 BETs each, in parallel across the instances | Exact balance and debits on all of them |
| REFUND before the BET (different instances) | Resolved by the worker of any instance; with `PENDING_MAX_ATTEMPTS=3` and no BET, it expires with `REFERENCE_NOT_FOUND` |
| Stop all three instances and start three new ones | Replays, a pending item created before the restart resolved after it, consistency |
| Same BET via HTTP and twice via SQS at the same time (10 Wallets) | One transaction and one debit; both Inbox Messages point to it |

At the end of each scenario: consistent Reconciliation (endpoint) and, via SQL, balance equal to credits minus debits, last `balance_after` equal to the balance and at most one Ledger Entry per transaction; every published Outbox Event reaches `wallet-events.fifo` exactly once, with `MessageGroupId` = Wallet, `MessageDeduplicationId` = `eventId` and in outbox order per Wallet.

```sh
go test -tags=e2e -race -timeout 10m ./test/e2e/...
scripts/test-race.sh -tags=e2e -timeout 10m ./test/e2e/...   # race without a C compiler on the host (Docker)
```

The suite takes about 50 seconds. Each process's logs are printed when a test fails, and a `WARNING: DATA RACE` in any instance fails the test. Stopping uses SIGTERM (exit 0 required) on Linux and macOS; on Windows a process cannot signal another, so stopping becomes `Kill`, and graceful shutdown is covered by the container run.

### Fault simulation

With the `faultinject` build tag, `crashpoint.Hit` reads `CRASH_AT` (one or more comma-separated names; an unknown name logs a warning and is ignored) and, on reaching the named point, logs `crashpoint hit, exiting` and exits the process with `os.Exit(137)`, without shutdown hooks. Without the tag, `Hit` is a no-op and the binary does not contain the hook (`internal/crashpoint/hit_test.go` builds `cmd/app` with and without the tag and checks the symbols with `go tool nm`).

Each scenario first starts an instance with only the `api` role, which creates the work without consuming it, and then an instance with only the role that reaches the crashpoint and `CRASH_AT` set, alone, so that only it can pick up the work. The test requires exit 137 and the crashpoint log line; then two healthy instances with all roles start.

| Crashpoint | Scenario | Guarantee |
| --- | --- | --- |
| `consumer.after_commit` | BET via SQS; the transaction and the Inbox Message are committed and the message is not deleted | With `CONSUMER_VISIBILITY_TIMEOUT=2s` the message reappears and a healthy instance recognizes it through the inbox (`wallet_inbox_duplicates_total` = 1): one transaction, one Inbox Message, one debit |
| `outbox.before_publish` | Events committed and claimed, nothing sent | With `OUTBOX_LEASE_DURATION=2s` the lease expires and the healthy instances publish every event |
| `outbox.after_publish` | First event sent to SQS, `published_at` not stored | The healthy instances resend with the same `eventId` and mark the outbox; FIFO deduplication drops the copy, so each `eventId` arrives exactly once |
| `pending.after_claim` | Pending REFUND claimed and abandoned | The lease (3s) expires, the BET arrives and a healthy instance resolves the REFUND: one credit, balance restored |

All end with a consistent Reconciliation and each Outbox Event delivered exactly once, in Wallet order. The suite with the tag also runs the multiple instance scenarios (about 60 seconds; each fault scenario takes 5 to 9 seconds):

```sh
go test -tags=e2e,faultinject -timeout 10m ./test/e2e/...
scripts/test-race.sh -tags=e2e,faultinject -timeout 10m ./test/e2e/...   # with race, in Docker
```

## Structure

```text
cmd/app/                 application entry point: loads the config and runs the Fx app
internal/config/         environment variables, validation and roles (APP_ROLES)
internal/bootstrap/      Fx composition root: infrastructure modules and role selection
internal/observability/  slog JSON logger and Prometheus registry
internal/health/         readiness checks (Fx value group) and /health/* handlers
internal/domain/         Money, Wallet, WagerTransaction, LedgerEntry, events (no infra dependencies)
internal/app/            use cases shared by HTTP and SQS
internal/postgres/       pgx pool, repositories and transaction boundaries
internal/httpapi/        HTTP server, handlers, authentication and authorization
internal/sqs/            SQS client, consumer and publisher
internal/worker/         outbox and pending references
internal/testsupport/    integration harness (build tags integration/e2e)
migrations/              versioned SQL migrations (golang-migrate), embedded for tests
deploy/                  queue init and Keycloak realm
scripts/                 development utilities (race in a container)
test/e2e/                multi-process and fault injection tests
```

## Quality

```sh
gofmt -l .
go vet ./...
go test -race ./...
```
