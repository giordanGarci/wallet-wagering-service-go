# 12: SQS consumer

**What to build:** um Provider envia operações por `wager-transactions.fifo` com as mesmas garantias do HTTP; reentregas são absorvidas, mensagens inválidas vão para a DLQ e nenhuma operação se perde em quedas ou deploys.

**Blocked by:** 06 (Idempotency and Replay)

**Status:** ready-for-agent

- [ ] Consumidor ativado pelo papel `consumer`, com long polling e credenciais próprias no broker
- [ ] Envelope `WagerTransactionRequested` validado; `data.idempotencyKey` usado como Idempotency Key; mesmo caso de uso e mesmo Payload Hash do HTTP
- [ ] Inbox Message com `UNIQUE (consumer_name, message_id)` e hash, gravada na mesma transação SQL da operação, do ledger e da outbox
- [ ] Mensagem removida somente após o commit; rejeições de negócio confirmadas também removem a mensagem
- [ ] Reentrega com mesmo `messageId` e mesmo hash: removida sem efeito; com hash diferente: enviada à DLQ
- [ ] JSON inválido, envelope desconhecido, validação de formato e OPENING: enviados explicitamente à DLQ e removidos da entrada
- [ ] Falha transitória: `ChangeMessageVisibility` com backoff exponencial; redrive para a DLQ após 5 recebimentos
- [ ] Reversal pendente conclui a Inbox Message após a Pending Reference persistida (quando o ticket 11 estiver pronto)
- [ ] Visibilidade estendida em processamentos longos; em `SIGTERM`, para o polling, aguarda o trabalho até o prazo e zera a visibilidade das não concluídas
- [ ] Métricas de mensagens na DLQ, duplicatas da inbox e retries
- [ ] Testes de integração: reentrega deduplicada, hash divergente na DLQ, mensagem malformada na DLQ, retry com PostgreSQL indisponível, shutdown com mensagem em andamento
- [ ] Teste de integração: a mesma operação enviada ao mesmo tempo por HTTP e SQS gera um único efeito financeiro
