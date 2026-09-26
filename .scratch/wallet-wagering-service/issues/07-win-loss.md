# 07: WIN and LOSS

**What to build:** um Provider credita prêmios com WIN, opcionalmente vinculados a uma BET da mesma Round, e registra LOSS sem movimentar a Wallet.

**Blocked by:** 06 (Idempotency and Replay)

**Status:** done

- [x] WIN com valor positivo credita a Wallet, gera Ledger Entry de crédito, `WagerTransactionProcessed` e `WalletBalanceChanged`
- [x] WIN com `referenceExternalTransactionId` resolve a Reference por `(providerId, referenceExternalTransactionId)` e exige BET da mesma Round, jogador, Wallet e moeda; divergência rejeita com `REFERENCE_MISMATCH`
- [x] LOSS exige `money.amount` igual a `"0.00"` e a moeda da Wallet; valor diferente de zero responde 400
- [x] LOSS processada gera apenas `WagerTransactionProcessed`, sem ledger, sem `WalletBalanceChanged` e sem mudança de versão
- [x] WIN com valor zero responde 400
- [x] Testes unitários da política de zero por tipo
- [x] Testes de integração de WIN, WIN com Reference e LOSS, incluindo Replay
