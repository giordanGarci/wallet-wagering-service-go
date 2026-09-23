# 14: Fault injection

**What to build:** um avaliador executa `go test -tags=e2e,faultinject` e vê uma instância morrer em pontos críticos enquanto outra retoma o trabalho sem duplicar movimentação nem perder eventos.

**Blocked by:** 13 (Three-instance e2e suite)

**Status:** ready-for-agent

- [ ] Hook de crashpoint compilado apenas com a build tag `faultinject`, ativado por `CRASH_AT`, encerra o processo com código 137
- [ ] `consumer.after_commit`: reentrega deduplicada pela inbox, efeito financeiro único
- [ ] `outbox.before_publish`: outra instância publica o evento
- [ ] `outbox.after_publish`: republicação preserva o `eventId`
- [ ] `pending.after_claim`: lease expira e outra instância conclui a Pending Reference
- [ ] Builds sem a tag não contêm o hook
- [ ] Reconciliation consistente ao final de cada cenário
