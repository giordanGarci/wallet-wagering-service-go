# 13: Three-instance e2e suite

**What to build:** um avaliador executa `go test -tags=e2e -race` e vê as garantias financeiras comprovadas com três processos independentes da aplicação, cada um com sua porta, pool e memória, contra a mesma infraestrutura.

**Blocked by:** 07 (WIN and LOSS), 08 (Reconciliation), 09 (Outbox publisher), 11 (Pending Reference and its worker), 12 (SQS consumer)

**Status:** ready-for-agent

- [ ] O teste compila o binário e sobe três processos com todos os papéis ativos
- [ ] A mesma BET enviada 50 vezes em paralelo, distribuída entre as instâncias, gera um único débito
- [ ] Duas BETs de 80.00 sobre 100.00 em instâncias diferentes: uma PROCESSED, uma `INSUFFICIENT_FUNDS`, saldo 20.00, um débito; reenvios mantêm o resultado
- [ ] Wallets distintas processadas simultaneamente
- [ ] Mesma operação cruzando HTTP e SQS entre instâncias
- [ ] Reversal antes da Reference resolvido por outra instância, e rejeitado por expiração
- [ ] Reinício de todas as instâncias preserva idempotência, pendências e consistência financeira
- [ ] Ao final de cada cenário: Reconciliation consistente e saldo igual a créditos menos débitos do ledger
- [ ] Cada Outbox Event esperado chega a `wallet-events.fifo`
