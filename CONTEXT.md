# Wallet & Wagering

Movimenta carteiras de jogadores a partir de operações enviadas por provedores de jogos, com ledger auditável e garantias de idempotência em ambiente distribuído.

## Language

### Carteira e dinheiro

**Money**:
Valor monetário exato em unidades mínimas acompanhado de um código de moeda ISO 4217.
_Avoid_: amount (sozinho), valor, price

**Wallet**:
Saldo de um jogador em uma moeda; existe no máximo uma por par (jogador, moeda).
_Avoid_: account, conta, carteira do provedor

**Ledger Entry**:
Registro imutável de um débito ou crédito em uma Wallet, com saldo anterior e posterior.
_Avoid_: movimento, lançamento contábil genérico, log

**House Account**:
Contrapartida por moeda que recebe o lançamento oposto de cada Ledger Entry, fazendo o journal somar zero.
_Avoid_: conta da empresa, caixa

**Reconciliation**:
Comparação entre o saldo armazenado de uma Wallet e o saldo reconstruído a partir de seus Ledger Entries.
_Avoid_: auditoria, recálculo

### Operações

**Provider**:
Parceiro externo que envia operações de jogo; identificado pelo token autenticado.
_Avoid_: cliente, parceiro, integrador

**Wager Transaction**:
Uma operação financeira sobre uma Wallet, de origem externa (Provider) ou interna (Opening).
_Avoid_: transação (sozinho, colide com transação SQL), aposta

**Opening**:
Wager Transaction interna que credita o saldo inicial positivo na abertura da Wallet.
_Avoid_: depósito, saldo inicial (como operação)

**Round**:
Rodada de um jogo à qual as operações externas pertencem.
_Avoid_: partida, sessão

**Reference**:
Wager Transaction anterior do mesmo Provider que uma operação precisa desfazer ou relacionar.
_Avoid_: original, parent

**Reversal**:
REFUND ou ROLLBACK: operação que desfaz integralmente uma Reference.
_Avoid_: estorno (ambíguo), cancelamento

**Compensation Slot**:
Direito único de uma BET de ser desfeita por um Reversal (REFUND ou ROLLBACK); uma vez consumido, nunca volta.
_Avoid_: estorno disponível

**Pending Reference**:
Estado de uma operação cuja Reference ainda não chegou ou ainda não terminou.
_Avoid_: órfã, aguardando

**Failure Code**:
Código estável que explica por que uma Wager Transaction foi REJECTED ou FAILED.
_Avoid_: erro, motivo

### Idempotência e mensageria

**Idempotency Key**:
Chave enviada pelo Provider, única dentro do Provider, que identifica uma tentativa de operação.
_Avoid_: request id, dedup id

**Payload Hash**:
Hash do JSON canônico dos campos de negócio de uma operação, igual para HTTP e SQS.
_Avoid_: checksum, assinatura

**Replay**:
Reenvio de uma operação já registrada, respondido com o resultado persistido original.
_Avoid_: retry (do lado do servidor), reprocessamento

**Inbox Message**:
Registro durável de uma mensagem SQS recebida por um consumidor, único por (consumidor, messageId).
_Avoid_: dedup table

**Outbox Event**:
Evento de integração gravado no mesmo commit que o originou e publicado depois por um worker.
_Avoid_: notificação, mensagem de saída
