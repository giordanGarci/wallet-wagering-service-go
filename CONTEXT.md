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
Contrapartida por moeda que recebe o lançamento oposto de cada Ledger Entry, fazendo o journal somar zero. Opcional no desafio e ainda não implementada (ver ARCHITECTURE.md).
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
Código estável que explica por que uma Wager Transaction foi REJECTED ou FAILED, classificado como corrigível ou definitivo.
_Avoid_: erro, motivo

**Permanent Error**:
Falha de infraestrutura que nenhuma nova tentativa resolve; a Wager Transaction termina FAILED com o Failure Code `PERMANENT_ERROR`, registrada para auditoria.
_Avoid_: erro fatal, exceção

**Transient Failure**:
Falha de infraestrutura que uma nova tentativa pode resolver (banco indisponível, timeout, deadlock, conflito de versão); nunca gera registro terminal.
_Avoid_: erro temporário (sozinho), falha

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
Registro durável de uma mensagem SQS recebida por um consumidor, único por (consumidor, messageId do envelope), com o hash do corpo para reconhecer reentregas.
_Avoid_: dedup table

**Outbox Event**:
Evento de integração gravado no mesmo commit que o originou e publicado depois por um worker.
_Avoid_: notificação, mensagem de saída

**Channel**:
Adaptador por onde uma operação externa chegou: HTTP ou SQS. Os dois executam o mesmo caso de uso com as mesmas regras.
_Avoid_: origem (reservado para externa ou interna), transporte

**Envelope**:
Objeto JSON que embrulha uma mensagem de fila: `WagerTransactionRequested` na entrada (com `messageId`, `type`, `occurredAt` e `data`) e o envelope de cada Outbox Event na saída (com `eventId`, `eventType`, `aggregateId` e `data`).
_Avoid_: payload (sozinho), wrapper

**Correlation ID**:
Identificador que acompanha uma operação da entrada (header `X-Correlation-Id` ou envelope SQS) até os logs e os Outbox Events que ela gera.
_Avoid_: request id, trace id

**Causation ID**:
Identificador da mensagem que causou um Outbox Event; é o `messageId` do Envelope quando a operação veio pela fila.
_Avoid_: parent id

**Dead-Letter Queue**:
Fila `wager-transactions-dlq.fifo`, que recebe as mensagens de entrada com erro permanente (envio explícito, com motivo) ou com falhas transitórias esgotadas (redrive após 5 recebimentos).
_Avoid_: fila de erros, lixeira

### Operação e recuperação

**Role**:
Componente que uma instância executa, escolhido por `APP_ROLES`: `api`, `consumer`, `outbox` ou `pending`.
_Avoid_: serviço, modo

**Lease**:
Posse temporária de uma linha reivindicada por um worker, registrada em `locked_until`; vencida, qualquer instância pode reivindicá-la de novo, e toda escrita do dono é condicionada a ela.
_Avoid_: lock (sozinho, colide com lock de linha), reserva

**Claim**:
Ato de um worker reservar um lote de linhas devidas sob um Lease, com `FOR UPDATE SKIP LOCKED`.
_Avoid_: poll, fetch

**Crashpoint**:
Ponto nomeado do código onde uma build de teste com a tag `faultinject` encerra o processo quando `CRASH_AT` o indica, para provar a recuperação entre duas etapas.
_Avoid_: breakpoint, fault
