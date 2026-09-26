# 09: Outbox publisher

**What to build:** Outbox Events confirmados chegam a `wallet-events.fifo` depois do commit, com `eventId` estável, mesmo com vários publishers disputando e com o SQS temporariamente indisponível.

**Blocked by:** 04 (Opening with a positive balance)

**Status:** done

- [x] Worker ativado pelo papel `outbox` em `APP_ROLES`, gerenciado por `fx.Lifecycle` com término observável
- [x] Claim com `FOR UPDATE SKIP LOCKED` sobre registros não publicados, com `next_attempt_at` vencido e lease (`locked_until`) expirado
- [x] Publicação com `MessageGroupId = aggregateId` e `MessageDeduplicationId = eventId`; `published_at` marcado depois do envio
- [x] Falha de publicação incrementa tentativas e agenda `next_attempt_at` com backoff exponencial e jitter
- [x] Payload publicado idêntico ao snapshot gravado no commit
- [x] No shutdown, o worker termina o lote corrente ou libera seus leases
- [x] Métricas de atraso da outbox (idade do mais antigo não publicado) e de retries
- [x] Teste de integração: dois publishers sobre a mesma outbox publicam cada evento com um único `eventId`
- [x] Teste de integração: lease abandonado é retomado por outro publisher preservando o `eventId`
- [x] Teste de integração: SQS indisponível gera retry com backoff e publicação posterior
