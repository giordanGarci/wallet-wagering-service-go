# 10: Reversals with the Reference available

**What to build:** um Provider envia REFUND ou ROLLBACK para uma Reference já processada e tem o valor integralmente compensado, com no máximo uma compensação por BET.

**Blocked by:** 06 (Idempotency and Replay)

**Status:** done

- [x] `referenceExternalTransactionId` obrigatório para REFUND e ROLLBACK; ausência responde 400
- [x] Reference resolvida apenas dentro do `providerId` do token
- [x] REFUND de BET processada credita o valor; ROLLBACK de BET credita; ROLLBACK de WIN debita; ROLLBACK de REFUND debita e mantém o Compensation Slot consumido (ADR-0002)
- [x] Índice único parcial sobre a BET compensada; segundo Reversal da mesma BET rejeitado com `REFERENCE_ALREADY_COMPENSATED`
- [x] ROLLBACK de ROLLBACK, REFUND de WIN e Reversal de LOSS rejeitados com `REFERENCE_NOT_REVERSIBLE`
- [x] Divergência em jogador, Wallet, moeda ou Round: `REFERENCE_MISMATCH`; valor diferente: `REFERENCE_AMOUNT_MISMATCH`
- [x] Reference REJECTED ou FAILED: `REFERENCE_UNSUCCESSFUL`
- [x] Reversal que debitaria além do saldo: 422 `REVERSAL_INSUFFICIENT_FUNDS`, distinto de `INSUFFICIENT_FUNDS`
- [x] Testes de integração das sequências: REFUND duplo, ROLLBACK de REFUND seguido de REFUND, ROLLBACK de ROLLBACK, dois Reversals simultâneos sobre a mesma BET
- [x] Reconciliation consistente ao final de cada sequência (quando o ticket 08 estiver pronto)

Notas de implementação:

- Testes em `internal/httpapi/reversals_integration_test.go` (seam HTTP) e `internal/postgres/transactions_integration_test.go`.
- Reference de outro provedor: a busca é por `(providerId do token, referenceExternalTransactionId)`, então a BET de outro provedor é uma Reference ausente e o Reversal fica em `PENDING_REFERENCE` (202), sem mover saldo nem consumir o slot (histórias 24 e 32 da spec). O worker do ticket 11 o rejeita com `REFERENCE_NOT_FOUND` ao esgotar as tentativas.
- `REFERENCE_MISMATCH` por moeda não é alcançável pelo HTTP: na mesma Wallet a moeda da Reference é a da Wallet (`CURRENCY_MISMATCH` vem antes), e em outra Wallet a divergência de Wallet aparece primeiro. Coberto no teste unitário do domínio.
- `REFERENCE_UNSUCCESSFUL` para Reference `FAILED` é coberto no domínio; pelo HTTP, com uma BET `REJECTED` (`INSUFFICIENT_FUNDS`).
- Correção: uma violação dos índices de compensação no `Insert` virava erro permanente (FAILED `PERMANENT_ERROR`, 500). Agora mapeia para `app.ErrCompensationConflict` (envolve `ErrDuplicateTransaction`): o caso de uso repete uma vez e responde `REFERENCE_ALREADY_COMPENSATED`.
