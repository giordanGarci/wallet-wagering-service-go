# 09: Outbox publisher

**What to build:** Outbox Events confirmados chegam a `wallet-events.fifo` depois do commit, com `eventId` estável, mesmo com vários publishers disputando e com o SQS temporariamente indisponível.

**Blocked by:** 04 (Opening with a positive balance)

**Status:** ready-for-agent

- [ ] Worker ativado pelo papel `outbox` em `APP_ROLES`, gerenciado por `fx.Lifecycle` com término observável
- [ ] Claim com `FOR UPDATE SKIP LOCKED` sobre registros não publicados, com `next_attempt_at` vencido e lease (`locked_until`) expirado
- [ ] Publicação com `MessageGroupId = aggregateId` e `MessageDeduplicationId = eventId`; `published_at` marcado depois do envio
- [ ] Falha de publicação incrementa tentativas e agenda `next_attempt_at` com backoff exponencial e jitter
- [ ] Payload publicado idêntico ao snapshot gravado no commit
- [ ] No shutdown, o worker termina o lote corrente ou libera seus leases
- [ ] Métricas de atraso da outbox (idade do mais antigo não publicado) e de retries
- [ ] Teste de integração: dois publishers sobre a mesma outbox publicam cada evento com um único `eventId`
- [ ] Teste de integração: lease abandonado é retomado por outro publisher preservando o `eventId`
- [ ] Teste de integração: SQS indisponível gera retry com backoff e publicação posterior
