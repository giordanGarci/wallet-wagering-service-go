# 15: Final documentation

**What to build:** outra pessoa reproduz a solução a partir de um checkout limpo e entende cada decisão técnica sem ler o código.

**Blocked by:** 14 (Fault injection)

**Status:** ready-for-agent

- [ ] ARCHITECTURE.md cobre dinheiro, transações e unidade de trabalho, idempotência e Payload Hash, locks, máquina de estados, Pending References, Reversals e Compensation Slot, inbox/outbox, SQS (`MessageGroupId`, `MessageDeduplicationId`, visibility, DLQ), autenticação, autorização, Fx, shutdown, Failure Codes, contratos HTTP e limitações
- [ ] `.env.example` com valores locais, sem segredos reais
- [ ] README validado contra o código: todos os comandos executados a partir de um checkout limpo e marcadores ⏳ removidos
- [ ] Novos ADRs: envio explícito à DLQ, Failure Codes com mapeamento HTTP, claim por lease com `SKIP LOCKED`
- [ ] `CONTEXT.md` atualizado com termos novos que surgiram na implementação
- [ ] `gofmt -l .` vazio, `go vet ./...` e `go test -race ./...` verdes
