# 02: Walking skeleton

**What to build:** um operador sobe o ambiente com `docker compose up --build` e obtém uma aplicação Go composta com Fx que responde `/health/live` e `/health/ready` (PostgreSQL e SQS), expõe `/metrics`, registra logs JSON e encerra de forma graciosa. O harness de testes de integração passa a existir.

**Blocked by:** None (can start immediately)

**Status:** done

- [x] Compose com `postgres`, `ministack`, `migrate` e `sqs-init`; `app` sobe depois das dependências saudáveis
- [x] `sqs-init` cria `wager-transactions.fifo`, `wager-transactions-dlq.fifo` com redrive (`maxReceiveCount=5`) e `wallet-events.fifo`
- [x] Migrations versionadas aplicáveis e reversíveis pelos comandos documentados no README
- [x] Configuração lida de variáveis de ambiente e validada no start; configuração inválida aborta a inicialização com erro claro
- [x] `APP_ROLES` seleciona os componentes ativos (neste ticket apenas `api`)
- [x] `/health/ready` responde indisponível quando PostgreSQL ou SQS estão fora
- [x] Logs JSON via `slog`; registry Prometheus exposto em `/metrics`
- [x] Shutdown: HTTP para de aceitar conexões, depois fecha o pool pgx e os clientes AWS, dentro de `SHUTDOWN_TIMEOUT`
- [x] Harness `-tags=integration`: sobe a aplicação Fx no processo de teste com banco isolado por execução e filas com sufixo aleatório
- [x] Teste de integração da composição Fx: start, health ready, stop sem goroutines ou conexões vazadas
- [x] Dockerfile declara a mesma versão de Go do `go.mod`
