# 12: SQS consumer

**What to build:** um Provider envia operações por `wager-transactions.fifo` com as mesmas garantias do HTTP; reentregas são absorvidas, mensagens inválidas vão para a DLQ e nenhuma operação se perde em quedas ou deploys.

**Blocked by:** 06 (Idempotency and Replay)

**Status:** done

- [x] Consumidor ativado pelo papel `consumer`, com long polling e credenciais próprias no broker
- [x] Envelope `WagerTransactionRequested` validado; `data.idempotencyKey` usado como Idempotency Key; mesmo caso de uso e mesmo Payload Hash do HTTP
- [x] Inbox Message com `UNIQUE (consumer_name, message_id)` e hash, gravada na mesma transação SQL da operação, do ledger e da outbox
- [x] Mensagem removida somente após o commit; rejeições de negócio confirmadas também removem a mensagem
- [x] Reentrega com mesmo `messageId` e mesmo hash: removida sem efeito; com hash diferente: enviada à DLQ
- [x] JSON inválido, envelope desconhecido, validação de formato e OPENING: enviados explicitamente à DLQ e removidos da entrada
- [x] Falha transitória: `ChangeMessageVisibility` com backoff exponencial; redrive para a DLQ após 5 recebimentos
- [x] Reversal pendente conclui a Inbox Message após a Pending Reference persistida (testado: linha PENDING_REFERENCE com `next_attempt_at` e Inbox Message concluída no mesmo commit; a retomada é do worker do ticket 11)
- [x] Visibilidade estendida em processamentos longos; em `SIGTERM`, para o polling, aguarda o trabalho até o prazo e zera a visibilidade das não concluídas
- [x] Métricas de mensagens na DLQ, duplicatas da inbox e retries
- [x] Testes de integração: reentrega deduplicada, hash divergente na DLQ, mensagem malformada na DLQ, retry com PostgreSQL indisponível, shutdown com mensagem em andamento
- [x] Teste de integração: a mesma operação enviada ao mesmo tempo por HTTP e SQS gera um único efeito financeiro
