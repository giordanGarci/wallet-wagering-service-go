# wallet-wagering-service
Go service that moves player wallets based on game provider operations (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), received over HTTP and SQS, with an append-only ledger, persistent idempotency, inbox/outbox and Keycloak authentication.
