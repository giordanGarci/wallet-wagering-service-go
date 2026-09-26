# 06: Idempotency and Replay

**What to build:** um Provider reenvia uma operação e recebe o resultado original, inclusive o saldo observado na época; conteúdo divergente para a mesma chave ou reuso do `externalTransactionId` com outra chave recebe conflito. A garantia sobrevive a reinícios.

**Blocked by:** 05 (BET over HTTP)

**Status:** done

- [x] Payload Hash SHA-256 hex sobre JSON canônico com chaves ordenadas dos campos de negócio da spec; UUIDs normalizados para minúsculas; Idempotency Key e metadados de transporte excluídos
- [x] Hash persistido na Wager Transaction; `UNIQUE (provider_id, idempotency_key)` e `UNIQUE (provider_id, external_transaction_id)`
- [x] Mesma chave e mesmo hash: Replay com `idempotentReplay: true`, mesmo status HTTP e mesmo saldo do processamento original, mesmo após novas movimentações
- [x] Replay de uma rejeição devolve o mesmo Failure Code
- [x] Mesma chave com hash diferente: 409 `IDEMPOTENCY_KEY_CONFLICT`
- [x] Mesmo `externalTransactionId` com outra chave: 409 `EXTERNAL_TRANSACTION_CONFLICT`
- [x] A chave recebida é usada como enviada, sem substituição
- [x] Corridas de inserção resolvidas pelas constraints: o perdedor relê o vencedor e responde como Replay ou conflito
- [x] Replay de operação de outro provedor responde 404
- [x] Testes unitários do Payload Hash (determinismo, ordem de chaves, exclusões)
- [x] Teste de integração: a mesma BET enviada 50 vezes em paralelo gera um único débito e 49 Replays
- [x] Teste de integração: idempotência preservada após reiniciar a aplicação
- [x] Métrica de duplicatas
