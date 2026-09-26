# 15: Final documentation

**What to build:** outra pessoa reproduz a solução a partir de um checkout limpo e entende cada decisão técnica sem ler o código.

**Blocked by:** 14 (Fault injection)

**Status:** done

- [x] ARCHITECTURE.md cobre dinheiro, transações e unidade de trabalho, idempotência e Payload Hash, locks, máquina de estados, Pending References, Reversals e Compensation Slot, inbox/outbox, SQS (`MessageGroupId`, `MessageDeduplicationId`, visibility, DLQ), autenticação, autorização, Fx, shutdown, Failure Codes, contratos HTTP e limitações
- [x] `.env.example` com valores locais, sem segredos reais
- [x] README validado contra o código: todos os comandos executados a partir de um checkout limpo e marcadores ⏳ removidos
- [x] Novos ADRs: envio explícito à DLQ, Failure Codes com mapeamento HTTP, claim por lease com `SKIP LOCKED` (0004-0006; também 0007 payload exato da outbox, 0008 ordem por Wallet no claim, 0009 papel `wallet_app`)
- [x] `CONTEXT.md` atualizado com termos novos que surgiram na implementação
- [x] `gofmt -l .` vazio, `go vet ./...` e `go test -race ./...` verdes

## Comments

- README validado em um clone limpo da branch `solution`: `docker compose up --build` (com `KEYCLOAK_PORT=8082`, porque a 8081 estava ocupada neste host), health, token, abertura, BET, Replay, REFUND, ROLLBACK rejeitado, WIN, LOSS, consultas, ledger, envio pela fila, reconciliation e `migrate version|down 1|up`. Seções de observabilidade (Jaeger/Prometheus), `--scale app=3` e teste de carga k6 foram removidas do README porque não existem no código.
- `go test -race` roda via `scripts/test-race.sh` (Docker), porque o host Windows não tem compilador C: unitários, `integration` e `e2e,faultinject` verdes.
- A ordem de shutdown divergia da spec (workers paravam antes do HTTP); corrigida em `bootstrap.Options` e fixada por `TestShutdownOrder`.
