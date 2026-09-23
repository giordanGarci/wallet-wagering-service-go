# 04: Opening with a positive balance

**What to build:** abrir uma Wallet com saldo inicial positivo gera uma Opening PROCESSED, seu Ledger Entry de crédito e os Outbox Events `WagerTransactionProcessed` e `WalletBalanceChanged` no mesmo commit. O serviço interno pagina o ledger da Wallet.

**Blocked by:** 01 (Money value object), 03 (Authenticated Wallet opening with zero balance)

**Status:** ready-for-agent

- [ ] Wallet, Opening, Ledger Entry e dois registros de outbox confirmados atomicamente; a versão da Wallet permanece 1
- [ ] Schema da Wager Transaction distingue origem interna e externa por `CHECK`; índice único parcial impede segunda Opening por Wallet
- [ ] Ledger Entry valida `balanceAfter = balanceBefore ± money` no construtor
- [ ] Banco impõe `UNIQUE (wallet_id, transaction_id)` no ledger; trigger rejeita `UPDATE` e `DELETE`; o papel da aplicação tem apenas `INSERT` e `SELECT` no ledger
- [ ] Unidade de trabalho delimita a transação SQL e entrega os repositórios participantes
- [ ] Outbox Events têm envelope com `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` opcional, `occurredAt` UTC RFC 3339, `version` e `data` tipado; tipo e versão fixados pelo construtor
- [ ] `GET /wallets/:walletId/ledger` com cursor opaco base64url, ordenação estável por `(created_at, id)`, `limit` padrão 50 e máximo 200
- [ ] Aberturas simultâneas para o mesmo par resultam em uma Wallet e um único crédito inicial
- [ ] Teste de integração com SQL direto comprovando que `UPDATE` e `DELETE` no ledger falham
- [ ] Testes unitários de domínio da Opening e do Ledger Entry
