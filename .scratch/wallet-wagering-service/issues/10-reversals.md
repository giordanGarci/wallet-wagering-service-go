# 10: Reversals with the Reference available

**What to build:** um Provider envia REFUND ou ROLLBACK para uma Reference já processada e tem o valor integralmente compensado, com no máximo uma compensação por BET.

**Blocked by:** 06 (Idempotency and Replay)

**Status:** ready-for-agent

- [ ] `referenceExternalTransactionId` obrigatório para REFUND e ROLLBACK; ausência responde 400
- [ ] Reference resolvida apenas dentro do `providerId` do token
- [ ] REFUND de BET processada credita o valor; ROLLBACK de BET credita; ROLLBACK de WIN debita; ROLLBACK de REFUND debita e mantém o Compensation Slot consumido (ADR-0002)
- [ ] Índice único parcial sobre a BET compensada; segundo Reversal da mesma BET rejeitado com `REFERENCE_ALREADY_COMPENSATED`
- [ ] ROLLBACK de ROLLBACK, REFUND de WIN e Reversal de LOSS rejeitados com `REFERENCE_NOT_REVERSIBLE`
- [ ] Divergência em jogador, Wallet, moeda ou Round: `REFERENCE_MISMATCH`; valor diferente: `REFERENCE_AMOUNT_MISMATCH`
- [ ] Reference REJECTED ou FAILED: `REFERENCE_UNSUCCESSFUL`
- [ ] Reversal que debitaria além do saldo: 422 `REVERSAL_INSUFFICIENT_FUNDS`, distinto de `INSUFFICIENT_FUNDS`
- [ ] Testes de integração das sequências: REFUND duplo, ROLLBACK de REFUND seguido de REFUND, ROLLBACK de ROLLBACK, dois Reversals simultâneos sobre a mesma BET
- [ ] Reconciliation consistente ao final de cada sequência (quando o ticket 08 estiver pronto)
