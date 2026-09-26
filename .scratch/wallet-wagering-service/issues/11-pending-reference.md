# 11: Pending Reference and its worker

**What to build:** um Provider envia um Reversal antes da Reference, recebe 202 com `PENDING_REFERENCE` e o Reversal é concluído automaticamente quando a Reference chega, ou rejeitado de forma definitiva após o limite de tentativas.

**Blocked by:** 10 (Reversals with the Reference available)

**Status:** done

- [x] Reference ausente ou ainda em Pending Reference: Wager Transaction confirmada como `PENDING_REFERENCE`, resposta 202 e `WagerTransactionPendingReference` na outbox
- [x] Replay de uma operação pendente devolve 202 com o estado atual
- [x] Worker ativado pelo papel `pending`, com claim por `FOR UPDATE SKIP LOCKED` e lease, e backoff exponencial entre tentativas
- [x] Reference PROCESSED: aplica o Reversal com a Wallet bloqueada e segue as regras do ticket 10
- [x] Reference REJECTED ou FAILED: rejeita com `REFERENCE_UNSUCCESSFUL`
- [x] Após `PENDING_MAX_ATTEMPTS` (padrão 8): REJECTED `REFERENCE_NOT_FOUND` e `WagerTransactionRejected`
- [x] Pendências retomadas após reinício da aplicação; lease abandonado é assumido por outra instância
- [x] No shutdown, o worker conclui a tentativa corrente ou libera o lease
- [x] Testes de integração: resolução posterior, expiração com limite reduzido, Reference que termina rejeitada, retomada após reinício
- [x] Métrica de retries de Pending Reference
