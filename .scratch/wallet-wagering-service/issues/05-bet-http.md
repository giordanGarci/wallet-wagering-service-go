# 05: BET over HTTP

**What to build:** um Provider autenticado envia uma BET por HTTP e tem a Wallet debitada, ou recebe uma rejeição de negócio estável. O Provider consulta suas próprias operações e nunca vê as de outro provedor.

**Blocked by:** 04 (Opening with a positive balance)

**Status:** ready-for-agent

- [ ] `POST /wagering/transactions` exige `Idempotency-Key` e token de Provider; `providerId` do corpo diferente do claim `provider_id` recebe 403 sem efeito financeiro
- [ ] Caso de uso `ProcessWagerTransaction` compartilhável por HTTP e SQS, recebendo um comando normalizado e a origem
- [ ] Processamento síncrono em uma transação SQL (ADR-0001): `SELECT ... FOR UPDATE` na Wallet e `UPDATE` com verificação de versão (ADR-0003); zero linhas afetadas conta como conflito de concorrência em métrica
- [ ] Máquina de estados da Wager Transaction no domínio com transições validadas e estados terminais imutáveis
- [ ] BET processada: 200 com `transactionId`, `status`, `balance`, `idempotentReplay: false`; Ledger Entry de débito; `WagerTransactionProcessed` e `WalletBalanceChanged` na outbox
- [ ] BET acima do saldo: 422 `INSUFFICIENT_FUNDS`, Wager Transaction REJECTED persistida, `WagerTransactionRejected` na outbox, sem ledger
- [ ] Rejeições `WALLET_NOT_FOUND`, `WALLET_PLAYER_MISMATCH` e `CURRENCY_MISMATCH`
- [ ] Validação de formato responde 400 sem registrar operação: Money inválido, campos ausentes, valor zero, tipo OPENING
- [ ] Falha transitória de banco responde 503 com `Retry-After`
- [ ] `GET /wagering/transactions/:transactionId` e `GET /providers/:providerId/wagering/transactions/:externalTransactionId`: transação de outro provedor responde 404; `providerId` da rota diferente do token responde 403
- [ ] Teste de integração: duas BETs simultâneas de 80.00 sobre 100.00 resultam em uma PROCESSED, uma REJECTED `INSUFFICIENT_FUNDS`, saldo 20.00 e um único débito
- [ ] Teste de integração: BETs em Wallets distintas avançam em paralelo
- [ ] Métrica de resultados por status; logs com `correlationId`, `transactionId`, `walletId` e `providerId`, sem payload financeiro completo
