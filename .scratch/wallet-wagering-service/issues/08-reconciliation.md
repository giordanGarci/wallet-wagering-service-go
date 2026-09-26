# 08: Reconciliation

**What to build:** o serviço interno dispara a Reconciliation de uma Wallet e recebe o saldo armazenado, o saldo reconstruído a partir do ledger, a diferença e a consistência. Divergências ficam visíveis para operação.

**Blocked by:** 04 (Opening with a positive balance)

**Status:** done

- [x] `POST /wallets/:walletId/reconciliation` restrito a `wallet-internal`
- [x] Leitura em transação `REPEATABLE READ READ ONLY`; nenhuma escrita
- [x] Resposta com `walletId`, `storedBalance`, `calculatedBalance`, `difference` (armazenado menos calculado), `consistent` e `checkedEntries`, incluindo a Opening
- [x] Divergência gera log de alerta com `walletId` e incrementa uma métrica dedicada
- [x] Teste de integração consistente após Opening e movimentações
- [x] Teste de integração com divergência provocada por SQL direto no saldo (com privilégio de teste), confirmando resposta, log e métrica
