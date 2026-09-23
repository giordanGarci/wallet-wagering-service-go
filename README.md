# Wallet & Wagering Service

Serviço em Go que movimenta carteiras de jogadores a partir de operações de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), recebidas por HTTP e SQS, com ledger append-only, idempotência persistente, inbox/outbox e autenticação via Keycloak.

> **Status: em construção.** Este README descreve o contrato de execução planejado. Cada seção será validada contra o código à medida que a implementação avança; comandos ainda não validados estão marcados com ⏳.

- Enunciado original: [CHALLENGE.md](CHALLENGE.md)
- Decisões técnicas: [ARCHITECTURE.md](ARCHITECTURE.md) ⏳
- Glossário do domínio: [CONTEXT.md](CONTEXT.md)
- Registros de decisão: [docs/adr/](docs/adr/)

## Pré-requisitos

| Ferramenta | Versão | Uso |
| --- | --- | --- |
| Docker + Docker Compose v2 | Docker Desktop recente | Infraestrutura local e testes de integração |
| Go | declarada em `go.mod` | Build e testes fora do container (`GOTOOLCHAIN=auto` baixa a versão certa) |
| curl + jq | qualquer | Exemplos de chamadas autenticadas |

## Serviços do ambiente local

| Serviço | Porta | Descrição |
| --- | --- | --- |
| `app` | 8080 | API HTTP, consumidor SQS, publisher da outbox e worker de referências pendentes |
| `postgres` | 5432 | PostgreSQL |
| `keycloak` | 8081 | IdP OAuth 2.0/OIDC com realm `wallet` provisionado automaticamente ⏳ |
| `ministack` | 4566 | Emulador AWS (SQS, imagem `ministackorg/ministack`), sem conta nem token |
| `sqs-init` | | Cria filas e redrive policy e encerra (`deploy/sqs-init/init.sh`) |
| `migrate` | | Aplica as migrations (CLI `golang-migrate`) e encerra |
| `jaeger` | 16686 | Tracing (profile `observability`) ⏳ |
| `prometheus` | 9090 | Métricas (profile `observability`) ⏳ |

## Variáveis de ambiente

Copie o exemplo e ajuste se necessário:

```sh
cp .env.example .env
```

| Variável | Exemplo | Padrão | Descrição |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | `:8080` | Endereço do servidor HTTP |
| `DATABASE_URL` | `postgres://wallet:wallet@postgres:5432/wallet?sslmode=disable` | obrigatória | Conexão PostgreSQL |
| `AWS_ENDPOINT_URL` | `http://ministack:4566` | endpoint real da AWS | Endpoint do emulador SQS |
| `AWS_REGION` | `us-east-1` | `us-east-1` | Região AWS |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `wallet-consumer` / `local-secret` | cadeia padrão do SDK | Credenciais do consumidor no broker |
| `SQS_INPUT_QUEUE_URL` | `http://ministack:4566/000000000000/wager-transactions.fifo` | obrigatória | Fila de entrada |
| `SQS_EVENTS_QUEUE_URL` | `http://ministack:4566/000000000000/wallet-events.fifo` | obrigatória | Fila de eventos publicados pela outbox |
| `OIDC_ISSUER_URL` | `http://keycloak:8081/realms/wallet` | | Issuer esperado nos tokens ⏳ |
| `OIDC_AUDIENCE` | `wallet-api` | | Audience exigida ⏳ |
| `APP_ROLES` | `api,consumer,outbox,pending` | todos | Componentes ativos nesta instância (lista separada por vírgula) |
| `PENDING_MAX_ATTEMPTS` | `8` | | Tentativas antes de rejeitar com `REFERENCE_NOT_FOUND` ⏳ |
| `SHUTDOWN_TIMEOUT` | `25s` | `25s` | Prazo para concluir trabalho em andamento no `SIGTERM` |
| `LOG_LEVEL` | `info` | `info` | Nível dos logs JSON: `debug`, `info`, `warn`, `error` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://jaeger:4318` | | Exportador de traces (opcional) ⏳ |

O `.env.example` contém apenas valores locais de exemplo, sem segredos reais. O compose carrega `.env.example` e, se existir, `.env` por cima dele. A configuração é validada antes de qualquer conexão: um valor inválido encerra o processo com status 2 e uma única mensagem listando todos os problemas.

Por enquanto só o papel `api` está implementado; os demais papéis são aceitos e registram um aviso `role not implemented yet` na subida. O servidor HTTP (health e `/metrics`) roda em todas as instâncias, qualquer que seja `APP_ROLES`.

## Executando

```sh
docker compose up --build
```

A ordem de inicialização é: `postgres`, `keycloak` ⏳ e `ministack` saudáveis, depois `migrate` e `sqs-init`, depois `app`. Verifique:

```sh
curl -s localhost:8080/health/live    # {"status":"ok"}
curl -s localhost:8080/health/ready   # {"status":"ok","checks":{"postgres":"up","sqs":"up"}}
curl -s localhost:8080/metrics        # formato Prometheus
```

`/health/ready` responde `503` com `"status":"unavailable"` e o check afetado como `"down"` quando o PostgreSQL ou alguma fila configurada não responde em 2s. Na subida, a aplicação falha cedo se o PostgreSQL ou as filas estiverem inacessíveis. No `SIGTERM`, o servidor HTTP para de aceitar conexões primeiro, depois o pool pgx e o cliente SQS são fechados, tudo dentro de `SHUTDOWN_TIMEOUT`.

O MiniStack não persiste estado: se o container `ministack` for reiniciado, recrie as filas com `docker compose run --rm sqs-init`.

Com observabilidade:

```sh
docker compose --profile observability up --build
```

Múltiplas instâncias da aplicação:

```sh
docker compose up --build --scale app=3
```

## Filas SQS

O container `sqs-init` provisiona automaticamente:

| Fila | Configuração |
| --- | --- |
| `wager-transactions.fifo` | Entrada; `VisibilityTimeout=30`, `ReceiveMessageWaitTimeSeconds=20`, redrive para a DLQ com `maxReceiveCount=5` |
| `wager-transactions-dlq.fifo` | Dead-letter queue da entrada |
| `wallet-events.fifo` | Destino dos eventos da outbox; `MessageGroupId=aggregateId`, `MessageDeduplicationId=eventId` |

Para recriar manualmente:

```sh
docker compose run --rm sqs-init
```

Mensagens de entrada usam `MessageGroupId = walletId` e `MessageDeduplicationId = messageId`. Detalhes em [ARCHITECTURE.md](ARCHITECTURE.md).

## Migrations

As migrations são versionadas em `migrations/` no formato do [golang-migrate](https://github.com/golang-migrate/migrate) (`NNNNNN_descricao.up.sql` e `NNNNNN_descricao.down.sql`, numeração sequencial de 6 dígitos) e aplicadas pelo serviço `migrate` na subida do compose, usando `DATABASE_URL`. Os testes de integração aplicam os mesmos arquivos, embutidos no pacote `migrations`.

```sh
# aplicar todas
docker compose run --rm migrate up

# reverter a última
docker compose run --rm migrate down 1

# versão atual
docker compose run --rm migrate version
```

## Autenticação

O realm `wallet` é importado automaticamente pelo Keycloak na subida. Todos os clients usam `client_credentials`.

| Client | Secret local | Permissões |
| --- | --- | --- |
| `provider-a` | `provider-a-secret` | Envia e consulta apenas transações de `provider-a` |
| `provider-b` | `provider-b-secret` | Envia e consulta apenas transações de `provider-b` |
| `wallet-internal` | `wallet-internal-secret` | Abertura, leitura, ledger e reconciliação de carteiras |

O `providerId` autorizado vem do claim `provider_id` do token. Um corpo com `providerId` diferente do token recebe `403`; uma transação de outro provedor responde `404`.

Obter um token:

```sh
token() {
  curl -s -X POST localhost:8081/realms/wallet/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-secret" \
    | jq -r .access_token
}
INTERNAL=$(token wallet-internal)
PROVIDER_A=$(token provider-a)
```

## Exemplos de chamadas

Abrir carteira:

```sh
curl -s -X POST localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

Enviar uma aposta (use o `id` retornado como `walletId`):

```sh
curl -s -X POST localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123",
       "playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"<walletId>",
       "roundId":"round-987","gameId":"fortune-chimp","kind":"BET",
       "money":{"amount":"25.00","currency":"BRL"}}'
```

Consultas:

```sh
curl -s localhost:8080/wallets/<walletId> -H "Authorization: Bearer $INTERNAL"
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
curl -s -X POST localhost:8080/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL"
curl -s localhost:8080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER_A"
```

Enviar pela fila:

```sh
docker compose exec ministack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id <walletId> --message-deduplication-id msg-123 \
  --message-body file:///fixtures/bet.json
```

Os códigos HTTP e corpos de resposta (400, 401, 403, 404, 409, 422, 202, 503) estão documentados em [ARCHITECTURE.md](ARCHITECTURE.md).

## Testes

| Suíte | Comando | Requer |
| --- | --- | --- |
| Unitários | `go test ./...` | Nada |
| Unitários com race | `go test -race ./...` | Nada (CGO habilitado) |
| Unitários com race, sem compilador C no host | `scripts/test-race.sh` | Docker |
| Análise estática | `go vet ./...` | Nada |
| Integração | `go test -tags=integration -race ./...` | Infraestrutura de teste |
| Integração com race, sem compilador C no host | `scripts/test-race.sh -tags=integration ./...` | Infraestrutura de teste |
| Múltiplas instâncias (3 processos) | `go test -tags=e2e -race ./test/e2e/...` | Infraestrutura de teste |
| Simulação de falhas | `go test -tags=e2e,faultinject ./test/e2e/...` | Infraestrutura de teste |

### Preparando a infraestrutura de teste

```sh
docker compose up -d postgres keycloak ministack migrate sqs-init
```

(`keycloak` entra no compose com a autenticação ⏳; até lá, omita-o.)

Os testes de integração criam um banco isolado por execução e filas com sufixo aleatório, então podem rodar em paralelo ao ambiente de desenvolvimento. O harness (`internal/testsupport`) sobe a aplicação Fx dentro do processo de teste e lê os endpoints destas variáveis:

| Variável | Padrão | Uso |
| --- | --- | --- |
| `TEST_DATABASE_URL` | `postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable` | Conexão administrativa para `CREATE`/`DROP DATABASE` do banco isolado |
| `TEST_AWS_ENDPOINT_URL` | `http://localhost:4566` | Endpoint SQS onde as filas isoladas são criadas |
| `TEST_LOG_LEVEL` | `warn` | Nível de log da aplicação durante os testes |

`scripts/test-race.sh` roda `go test -race` dentro da imagem `golang` da mesma versão do `go.mod` (o race detector exige CGO); com a tag `integration` ou `e2e`, o container entra na rede `wallet_default` do compose e aponta essas variáveis para `postgres` e `ministack`.

### Múltiplas instâncias

Os testes com a tag `e2e` compilam o binário e sobem três processos independentes, cada um com sua porta, pool de conexões e memória, contra a mesma infraestrutura. Eles cobrem aposta duplicada 50 vezes, disputa das duas apostas de 80.00 sobre 100.00, carteiras distintas em paralelo e cenários cruzando HTTP e SQS.

### Simulação de falhas

Com a tag `faultinject`, a variável `CRASH_AT` encerra o processo com `os.Exit(137)` em um ponto nomeado. O teste derruba uma instância e confirma que outra retoma o trabalho.

| Crashpoint | Cenário |
| --- | --- |
| `consumer.after_commit` | Commit feito, mensagem ainda na fila: reentrega é deduplicada pela inbox |
| `outbox.after_publish` | Evento publicado, outbox não marcada: republicação preserva o `eventId` |
| `outbox.before_publish` | Evento confirmado e não publicado: outra instância publica |
| `pending.after_claim` | Pendência reivindicada e abandonada: lease expira e outra instância retoma |

### Teste de carga

```sh
docker compose --profile load run --rm k6
```

Metodologia, ambiente e resultados (throughput, p50/p95/p99, erros, conflitos e atraso da outbox) ficam em [docs/load-test.md](docs/load-test.md) ⏳.

## Estrutura

```text
cmd/app/                 entrada da aplicação: carrega a config e roda o app Fx
internal/config/         variáveis de ambiente, validação e papéis (APP_ROLES)
internal/bootstrap/      raiz de composição Fx: módulos de infraestrutura e seleção de papéis
internal/observability/  logger slog JSON e registry Prometheus
internal/health/         checks de readiness (value group Fx) e handlers /health/*
internal/domain/         Money, Wallet, WagerTransaction, LedgerEntry, eventos (sem dependências de infra)
internal/app/            casos de uso compartilhados por HTTP e SQS
internal/postgres/       pool pgx, repositórios e delimitação de transação
internal/httpapi/        servidor HTTP, handlers, autenticação e autorização
internal/sqs/            cliente SQS, consumidor e publisher
internal/worker/         outbox e referências pendentes
internal/testsupport/    harness de integração (build tags integration/e2e)
migrations/              migrations SQL versionadas (golang-migrate), embutidas para os testes
deploy/                  init de filas, realm Keycloak, Prometheus, k6
scripts/                 utilitários de desenvolvimento (race em container)
test/e2e/                testes com múltiplos processos e injeção de falhas
```

## Qualidade

```sh
gofmt -l .
go vet ./...
go test -race ./...
```
