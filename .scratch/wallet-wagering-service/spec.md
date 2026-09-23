# Spec: Wallet & Wagering Service

Status: ready-for-agent

## Problem Statement

Provedores de jogos precisam movimentar a Wallet de um jogador com cada BET, WIN, LOSS, REFUND e ROLLBACK que enviam, por HTTP ou por SQS, em um ambiente com várias instâncias, entrega at-least-once e falhas a qualquer momento. Hoje o repositório tem apenas o enunciado, o glossário, três ADRs e os testes de Money escritos antes da implementação. Sem o serviço, um Provider que reenvia uma operação, envia um Reversal antes da Reference ou dispara apostas simultâneas na mesma Wallet fica sem garantia de saldo correto, de ledger auditável ou de resposta estável.

A equipe interna, por sua vez, precisa abrir Wallets, consultar saldo e ledger e confirmar por Reconciliation que o saldo armazenado bate com os Ledger Entries. Os consumidores de eventos precisam receber cada Outbox Event pelo menos uma vez, com `eventId` estável, somente depois do commit que o originou.

## Solution

Um serviço Go composto com Uber Fx que expõe uma API HTTP autenticada por Keycloak (`client_credentials`), consome `wager-transactions.fifo`, publica Outbox Events em `wallet-events.fifo` e retoma Pending References por worker. HTTP e SQS chamam o mesmo caso de uso, com as mesmas regras de idempotência e o mesmo Payload Hash.

Cada Wager Transaction sem dependência pendente é validada, aplicada e concluída em uma única transação SQL (ADR-0001). A Wallet é serializada por lock pessimista de linha com verificação de versão (ADR-0003). Cada BET tem um único Compensation Slot (ADR-0002). O PostgreSQL impõe unicidade, saldo não negativo e imutabilidade do ledger por constraints e triggers, independentemente de qualquer coordenação na aplicação.

Qualquer instância pode assumir qualquer papel (`APP_ROLES=api,consumer,outbox,pending`), e o resultado financeiro permanece correto com três processos independentes e quedas entre as etapas.

## User Stories

### Provider: envio de operações

1. Como Provider, quero enviar uma BET por HTTP com `Idempotency-Key`, para debitar a Wallet do jogador e receber o saldo resultante.
2. Como Provider, quero enviar uma WIN com valor positivo, para creditar o prêmio na Wallet do jogador.
3. Como Provider, quero informar opcionalmente uma BET da mesma Round como Reference de uma WIN, para vincular o prêmio à aposta.
4. Como Provider, quero enviar uma LOSS com `money.amount` igual a `"0.00"`, para registrar o fim da Round sem movimentar a Wallet.
5. Como Provider, quero enviar um REFUND com `referenceExternalTransactionId`, para devolver integralmente uma BET processada.
6. Como Provider, quero enviar um ROLLBACK com `referenceExternalTransactionId`, para desfazer integralmente uma BET, WIN ou REFUND processada.
7. Como Provider, quero enviar as mesmas operações pela fila `wager-transactions.fifo`, para integrar de forma assíncrona com as mesmas garantias do HTTP.
8. Como Provider, quero que uma BET sem saldo suficiente seja rejeitada com um Failure Code estável, para informar o jogador de forma consistente.
9. Como Provider, quero que valores com escala diferente de duas casas, notação científica, `NaN`, `Infinity`, vazios ou negativos sejam rejeitados com 400, para detectar erros de integração cedo.
10. Como Provider, quero que valores zero em BET, WIN, REFUND e ROLLBACK sejam rejeitados, e que LOSS com valor diferente de zero também seja, para seguir a política de zero documentada.
11. Como Provider, quero que o tipo OPENING seja rejeitado quando enviado por mim, para que só o serviço interno crie créditos iniciais.
12. Como Provider, quero que uma operação cuja moeda difere da Wallet seja rejeitada, para impedir conversões implícitas.
13. Como Provider, quero que uma operação cujo `playerId` não corresponde à Wallet seja rejeitada, para impedir movimentação na Wallet errada.
14. Como Provider, quero que um Reversal cujo valor, jogador, Wallet, moeda ou Round divirja da Reference seja rejeitado, para impedir compensações incoerentes.
15. Como Provider, quero que um Reversal que precisaria debitar mais que o saldo disponível seja rejeitado com um código diferente do usado para BET sem saldo, para distinguir as duas situações na minha operação.

### Provider: idempotência e Replay

16. Como Provider, quero reenviar a mesma operação com a mesma Idempotency Key e o mesmo conteúdo e receber o resultado original com `idempotentReplay: true`, para repetir chamadas com segurança após timeouts.
17. Como Provider, quero que o Replay devolva o saldo observado no processamento original, mesmo que a Wallet tenha mudado depois, para que minhas conciliações sejam determinísticas.
18. Como Provider, quero receber 409 ao reutilizar uma Idempotency Key com conteúdo diferente, para detectar bugs de geração de chave.
19. Como Provider, quero receber 409 ao reenviar o mesmo `externalTransactionId` com outra Idempotency Key, para que a mesma operação nunca seja aplicada duas vezes.
20. Como Provider, quero que o Replay funcione igual entre HTTP e SQS, para poder alternar o canal sem duplicar movimentação.
21. Como Provider, quero que a idempotência sobreviva ao reinício de todas as instâncias, para confiar nela em deploys e quedas.
22. Como Provider, quero que o servidor use a Idempotency Key que eu enviei sem substituí-la por uma calculada, para rastrear minhas tentativas pela minha chave.
23. Como Provider, quero que o Replay de uma operação rejeitada devolva o mesmo Failure Code, para tratar a resposta de forma idempotente.

### Provider: References e Pending Reference

24. Como Provider, quero enviar um Reversal antes da Reference e receber 202 com status `PENDING_REFERENCE`, para não precisar ordenar minhas mensagens.
25. Como Provider, quero que o Reversal pendente seja concluído automaticamente quando a Reference chegar e for processada, para não precisar reenviá-lo.
26. Como Provider, quero que um Reversal pendente seja rejeitado com `REFERENCE_NOT_FOUND` após o limite de tentativas, para ter um resultado definitivo.
27. Como Provider, quero que um Reversal cuja Reference terminou REJECTED ou FAILED seja rejeitado com um código próprio, para distinguir Reference ausente de Reference sem sucesso.
28. Como Provider, quero que um Reversal cuja Reference ainda está em Pending Reference permaneça pendente, para que a cadeia se resolva na ordem certa.
29. Como Provider, quero que uma BET aceite no máximo um Reversal bem-sucedido, e que um segundo receba `REFERENCE_ALREADY_COMPENSATED`, para impedir devolução duplicada do mesmo débito.
30. Como Provider, quero poder fazer ROLLBACK de um REFUND, debitando o valor de volta, para corrigir um REFUND enviado por engano.
31. Como Provider, quero que um ROLLBACK de ROLLBACK seja rejeitado, para evitar cadeias de compensação sem fim.
32. Como Provider, quero que References sejam resolvidas apenas dentro do meu `providerId`, para que outro provedor nunca interfira nas minhas operações.

### Provider: consultas

33. Como Provider, quero consultar uma operação por `externalTransactionId`, para acompanhar pendências e ver Failure Codes.
34. Como Provider, quero receber 404 ao consultar uma operação de outro provedor, para que a existência dela nunca seja exposta.
35. Como Provider, quero consultar uma operação pelo `transactionId` interno devolvido no envio, para acompanhar o resultado por esse identificador.

### Autenticação e autorização

36. Como Provider, quero obter um token via `client_credentials` no Keycloak, para chamar a API sem gerenciar senhas no serviço.
37. Como operador de segurança, quero que requisições sem token, com token inválido, expirado, de outro issuer ou sem a audience `wallet-api` recebam 401, para bloquear acessos não autenticados.
38. Como operador de segurança, quero que o `providerId` autorizado venha do claim `provider_id` do token, e que um corpo com `providerId` diferente receba 403 sem efeito financeiro, para impedir que um provedor opere em nome de outro.
39. Como operador de segurança, quero que abertura, leitura de Wallet, ledger e Reconciliation exijam o client `wallet-internal`, para restringir operações de carteira ao serviço interno.
40. Como operador de segurança, quero que tokens de Provider recebam 403 nos endpoints internos, para separar os papéis.
41. Como operador de segurança, quero que o acesso ao SQS use credenciais e políticas próprias do consumidor, para controlar quem publica e consome.
42. Como operador de segurança, quero que o consumidor aplique as mesmas validações de domínio que o HTTP, para que a fila nunca seja um atalho para contornar regras.

### Serviço interno: Wallets

43. Como serviço interno, quero abrir uma Wallet para um jogador e uma moeda com saldo inicial, para habilitar o jogador a apostar.
44. Como serviço interno, quero que um saldo inicial positivo gere uma Opening PROCESSED, seu Ledger Entry de crédito e os Outbox Events `WagerTransactionProcessed` e `WalletBalanceChanged` no mesmo commit, para que a abertura seja auditável.
45. Como serviço interno, quero que saldo inicial zero crie a Wallet sem Opening, ledger ou eventos financeiros, para evitar lançamentos vazios.
46. Como serviço interno, quero receber 409 ao tentar abrir uma segunda Wallet para o mesmo jogador e moeda, para garantir uma Wallet por par.
47. Como serviço interno, quero que duas aberturas simultâneas para o mesmo par resultem em uma Wallet e um único crédito inicial, para impedir Opening duplicada.
48. Como serviço interno, quero consultar uma Wallet com saldo e versão, para exibir o estado atual.
49. Como serviço interno, quero listar os Ledger Entries de uma Wallet com cursor opaco e ordenação estável, para auditar o histórico em páginas.
50. Como serviço interno, quero disparar a Reconciliation de uma Wallet e receber saldo armazenado, saldo calculado, diferença, consistência e número de entradas verificadas, para confirmar a integridade do ledger.
51. Como serviço interno, quero que a Reconciliation leia uma visão consistente e nunca altere o saldo, para usá-la com segurança em produção.
52. Como operador, quero que divergências de Reconciliation apareçam na resposta, nos logs e em uma métrica, para ser alertado.

### Concorrência

53. Como Provider, quero que duas BETs simultâneas de 80.00 sobre saldo de 100.00 resultem em uma PROCESSED, uma REJECTED por saldo insuficiente, saldo 20.00 e um único débito, para confiar no controle de saldo.
54. Como Provider, quero que a mesma BET enviada 50 vezes em paralelo gere um único débito, para que retries agressivos sejam seguros.
55. Como Provider, quero que Wallets diferentes sejam processadas em paralelo, para que a latência de um jogador não afete outro.
56. Como operador, quero que essas garantias valham com três instâncias independentes, cada uma com suas conexões e memória, para escalar horizontalmente.
57. Como operador, quero que a mesma operação chegando por HTTP e SQS ao mesmo tempo seja aplicada uma vez, para que a escolha de canal nunca duplique movimentação.

### Consumidor SQS

58. Como operador, quero que a mensagem seja removida da fila somente após o commit do tratamento durável, para nunca perder uma operação.
59. Como operador, quero que uma reentrega com o mesmo `messageId` e o mesmo hash seja reconhecida pela Inbox Message e removida sem efeito, para absorver entregas duplicadas.
60. Como operador, quero que uma reentrega com o mesmo `messageId` e hash diferente seja tratada como erro permanente e vá para a DLQ, para investigar produtores inconsistentes.
61. Como operador, quero que rejeições de negócio confirmadas removam a mensagem, para que a fila avance.
62. Como operador, quero que mensagens malformadas, de tipo desconhecido ou com OPENING sejam enviadas à `wager-transactions-dlq.fifo`, para isolá-las sem bloquear o grupo.
63. Como operador, quero que falhas transitórias de PostgreSQL retornem a mensagem com backoff e cheguem à DLQ após `maxReceiveCount=5`, para tolerar indisponibilidades curtas.
64. Como operador, quero que um Reversal pendente conclua sua Inbox Message após a Pending Reference estar persistida, para que o worker assuma a continuidade.
65. Como operador, quero que, em `SIGTERM`, o consumidor pare de buscar mensagens e conclua ou libere a visibilidade das que estão em andamento dentro de `SHUTDOWN_TIMEOUT`, para permitir deploys sem perda.

### Outbox e eventos

66. Como consumidor de eventos, quero receber `WagerTransactionProcessed` para cada operação concluída, inclusive LOSS e Opening, para manter projeções atualizadas.
67. Como consumidor de eventos, quero receber `WagerTransactionRejected` com o Failure Code para cada rejeição definitiva, para reagir a falhas de negócio.
68. Como consumidor de eventos, quero receber `WalletBalanceChanged` com `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter` e `walletVersion` a cada mudança de saldo, para acompanhar saldos em tempo real.
69. Como consumidor de eventos, quero receber `WagerTransactionPendingReference` quando uma operação passa a esperar sua Reference, para acompanhar pendências.
70. Como consumidor de eventos, quero um envelope com `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` opcional, `occurredAt` UTC RFC 3339, `version` e `data` tipado, para desserializar de forma estável.
71. Como consumidor de eventos, quero que republicações preservem o `eventId`, para deduplicar do meu lado.
72. Como consumidor de eventos, quero que eventos de uma mesma Wallet saiam no mesmo `MessageGroupId`, para processá-los em ordem.
73. Como operador, quero que nenhum evento seja publicado antes do commit que o originou, para que eventos sempre reflitam estado confirmado.
74. Como operador, quero que vários publishers disputem a outbox sem publicar o mesmo registro em paralelo e que registros abandonados sejam retomados após o lease, para tolerar quedas entre publicação e confirmação.
75. Como operador, quero que falhas de publicação sejam retentadas com backoff exponencial registrando tentativas e próximo envio, para absorver indisponibilidade do SQS.
76. Como operador, quero que o payload da outbox seja um snapshot imutável gravado no commit, para que republicações tragam exatamente o mesmo conteúdo.

### Worker de Pending References

77. Como operador, quero que Pending References sejam retentadas com backoff exponencial por qualquer instância, inclusive após reinício, para que nenhuma fique esquecida.
78. Como operador, quero que uma instância que reivindicou uma Pending Reference e caiu tenha o lease expirado e o trabalho assumido por outra, para tolerar quedas no meio da retomada.
79. Como operador, quero configurar o limite de tentativas por `PENDING_MAX_ATTEMPTS`, para ajustar o TTL das pendências.

### Operação, ciclo de vida e observabilidade

80. Como operador, quero subir todo o ambiente com `docker compose up --build`, incluindo Keycloak com realm provisionado, filas com redrive e migrations aplicadas, para reproduzir a solução a partir de um checkout limpo.
81. Como operador, quero aplicar e reverter migrations por comandos documentados, para evoluir o schema com segurança.
82. Como operador, quero que a aplicação valide configuração e dependências na inicialização e falhe cedo, para detectar erros de ambiente antes de aceitar tráfego.
83. Como operador, quero que o shutdown interrompa novas entradas, conclua ou libere o trabalho em andamento e feche conexões somente depois dos componentes que as usam, para encerrar sem corromper estado.
84. Como operador, quero `/health/live` para o processo e `/health/ready` para PostgreSQL e SQS, sem autenticação, para integrar com orquestradores.
85. Como operador, quero logs JSON com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`, sem credenciais nem payloads financeiros completos, para rastrear uma operação sem vazar dados.
86. Como operador, quero métricas de resultados por status, duplicatas, retries, DLQ, conflitos de concorrência, atraso da outbox, latência e divergências de Reconciliation, para monitorar a saúde financeira.
87. Como operador, quero escolher por `APP_ROLES` quais componentes cada instância executa, para separar API, consumidor e workers quando necessário.
88. Como avaliador, quero respostas HTTP distinguíveis para entrada inválida (400), não autenticado (401), proibido (403), inexistente (404), conflito (409), rejeição de negócio (422), processamento pendente (202) e indisponibilidade transitória (503), para entender o contrato sem ler código.
89. Como avaliador, quero um ARCHITECTURE.md com as decisões sobre dinheiro, transações, idempotência, locks, Pending References, Reversals, inbox/outbox, autenticação, autorização, Fx e shutdown, incluindo limitações, para avaliar as escolhas.

## Implementation Decisions

### Módulos

- **Domínio** (sem dependência de Fx, HTTP, SQS ou pgx): Money e Currency; Wallet; Wager Transaction com sua máquina de estados; Ledger Entry; eventos de domínio com construtores que fixam tipo e versão; erros de domínio classificáveis por `errors.Is`/`errors.As`.
- **Aplicação**: casos de uso `OpenWallet`, `ProcessWagerTransaction` (compartilhado por HTTP e SQS, recebendo um comando já normalizado e a origem), `ResumePendingReference`, `Reconcile` e consultas. Um contrato de unidade de trabalho delimita a transação SQL e entrega os repositórios que participam dela.
- **Postgres**: repositórios com pgx e SQL explícito, implementação da unidade de trabalho, mapeamento de erros de constraint para erros de domínio e classificação de erros transitórios.
- **HTTP API**: roteamento, validação de JWT via JWKS do Keycloak, extração de `provider_id`, autorização por endpoint, mapeamento de resultados e erros para status e corpos documentados.
- **SQS**: consumidor com long polling, Inbox Message, extensão e liberação de visibilidade, envio explícito para DLQ; publisher de Outbox Events.
- **Workers**: publisher da outbox e worker de Pending References, ambos com claim por lease.
- **Composição**: um `fx.Module` por área, `fx.Lifecycle` para servidor e workers, seleção por `APP_ROLES`.
- **Crashpoints**: um hook nomeado compilado apenas com a build tag `faultinject`, ativado por `CRASH_AT`, que encerra o processo com código 137.

### Money

- `int64` em unidades mínimas, escala fixa de duas casas, moeda ISO 4217 validada contra uma lista conhecida. Limites: de -92233720368547758.08 a 92233720368547758.07.
- Parsing aceita apenas a forma canônica `^(0|[1-9][0-9]*)\.[0-9]{2}$`, sem normalização; por isso o Payload Hash usa a string recebida.
- Erros sentinela: `ErrInvalidAmount`, `ErrInvalidCurrency`, `ErrUninitialized`, `ErrCurrencyMismatch`, `ErrOverflow`. O valor zero do tipo é inválido.
- JSON estrito `{"amount":"25.00","currency":"BRL"}`: rejeita números JSON, campos ausentes, campos extras e `null`.
- Persistência em `BIGINT` de unidades mínimas mais `CHAR(3)` de moeda.
- A interface pública já está fixada pelos testes existentes do pacote Money (`Parse`, `ParseCurrency`, `FromMinorUnits`, `Zero`, `Add`, `Sub`, `Neg`, `Compare`, `Equal`, `IsZero`, `IsNegative`, `IsValid`, `Amount`, `MinorUnits`, `Currency`).

### Wallet e concorrência (ADR-0003)

- Criação e reidratação separadas; a reidratação apenas restaura estado.
- Débito e crédito são métodos do agregado que devolvem o Ledger Entry correspondente e incrementam a versão; a versão inicial é 1.
- Dentro da transação: `SELECT ... FOR UPDATE` na linha da Wallet, aplicação no agregado, `UPDATE ... WHERE id = $1 AND version = $esperada`. Zero linhas afetadas é tratado como conflito de concorrência e contabilizado em métrica.
- Schema: `UNIQUE (player_id, currency)` e `CHECK (balance >= 0)`.

### Wager Transaction

- Estados e transições válidas:

  ```
  PENDING            -> PROCESSED | REJECTED | FAILED | PENDING_REFERENCE
  PENDING_REFERENCE  -> PROCESSED | REJECTED | FAILED
  PROCESSED, REJECTED, FAILED: terminais
  ```

- Processamento síncrono (ADR-0001): o registro nasce e termina na mesma transação SQL; apenas `PENDING_REFERENCE` é confirmado como estado intermediário.
- A tabela distingue origem interna e externa por uma coluna de origem com `CHECK` que exige os metadados externos (provider, ids externos, chave, hash, Round, jogo) apenas para origem externa e os proíbe para Opening. Um índice único parcial garante no máximo uma Opening por Wallet.
- Unicidade externa: `UNIQUE (provider_id, idempotency_key)` e `UNIQUE (provider_id, external_transaction_id)`.
- A transação persiste o resultado devolvido ao Provider (status, saldo após o processamento, Failure Code), para que o Replay leia sem recalcular.
- Compensation Slot (ADR-0002): coluna com a BET compensada e índice único parcial sobre ela para REFUND e ROLLBACK de BET. ROLLBACK de REFUND debita de volta e mantém o slot consumido.
- FAILED registra falha permanente de infraestrutura para auditoria. Falhas transitórias (conexão, timeout, serialização, deadlock) nunca geram registro terminal; o chamador recebe 503 ou a mensagem volta à fila.

### Failure Codes

Códigos estáveis, cada um marcado como corrigível (o Provider pode reenviar com outra operação corrigida) ou definitivo:

| Código | Classe | Situação |
| --- | --- | --- |
| `INSUFFICIENT_FUNDS` | definitivo | BET acima do saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | definitivo | Reversal que debitaria além do saldo |
| `WALLET_NOT_FOUND` | corrigível | Wallet inexistente |
| `WALLET_PLAYER_MISMATCH` | corrigível | `playerId` diverge da Wallet |
| `CURRENCY_MISMATCH` | corrigível | Moeda diverge da Wallet |
| `REFERENCE_NOT_FOUND` | definitivo | Tentativas de Pending Reference esgotadas |
| `REFERENCE_UNSUCCESSFUL` | definitivo | Reference terminou REJECTED ou FAILED |
| `REFERENCE_MISMATCH` | corrigível | Reference diverge em jogador, Wallet, moeda ou Round |
| `REFERENCE_AMOUNT_MISMATCH` | corrigível | Valor do Reversal difere da Reference |
| `REFERENCE_NOT_REVERSIBLE` | definitivo | Reference de tipo não reversível para essa operação (ex.: ROLLBACK de ROLLBACK, REFUND de WIN) |
| `REFERENCE_ALREADY_COMPENSATED` | definitivo | Compensation Slot já consumido |

Validações de formato (Money inválido, campos ausentes, OPENING externo, LOSS com valor diferente de zero, valor zero onde é proibido) respondem 400 sem registrar Wager Transaction.

### Idempotência e Payload Hash

- Hash SHA-256 em hexadecimal sobre JSON canônico com chaves ordenadas dos campos `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId` (omitido quando ausente). UUIDs são normalizados para minúsculas antes do hash. Idempotency Key e metadados de transporte ficam fora.
- Mesma chave e mesmo hash: Replay com `idempotentReplay: true`, mesmo status HTTP do resultado original.
- Mesma chave e hash diferente: 409 `IDEMPOTENCY_KEY_CONFLICT`.
- Mesmo `externalTransactionId` com outra chave: 409 `EXTERNAL_TRANSACTION_CONFLICT`.
- Corridas de inserção são resolvidas pelas constraints únicas: o perdedor relê o registro vencedor e responde como Replay ou conflito.

### Contratos HTTP

- `POST /wallets`, `GET /wallets/:walletId`, `GET /wallets/:walletId/ledger?cursor=&limit=`, `POST /wallets/:walletId/reconciliation`: client `wallet-internal`.
- `POST /wagering/transactions`, `GET /wagering/transactions/:transactionId`, `GET /providers/:providerId/wagering/transactions/:externalTransactionId`: clients de Provider, sempre filtrados pelo `provider_id` do token; recurso de outro provedor responde 404; `providerId` do corpo ou da rota diferente do token responde 403.
- Resposta de envio: `transactionId`, `status`, `balance`, `failureCode` quando houver, `idempotentReplay`. Status HTTP: 200 PROCESSED, 422 REJECTED, 202 PENDING_REFERENCE, 503 transitório com `Retry-After`.
- Erros usam um corpo único com `code` e `message` estáveis.
- Cursor do ledger: base64url opaco da posição em ordenação estável por `(created_at, id)`; `limit` padrão 50, máximo 200.
- Reconciliation executa em transação `REPEATABLE READ READ ONLY`; `difference = storedBalance - calculatedBalance`.

### Ledger e House Account

- Cada Ledger Entry registra Wallet, Wager Transaction, direção, valor, saldo anterior e posterior; o construtor valida `balanceAfter = balanceBefore ± money`.
- `UNIQUE (wallet_id, transaction_id)`; trigger que rejeita `UPDATE` e `DELETE`; o papel de banco da aplicação recebe apenas `INSERT` e `SELECT` no ledger.
- LOSS e rejeições não geram Ledger Entry nem alteram a versão.
- Partidas dobradas com House Account por moeda são opcionais; entram somente se não atrasarem as garantias obrigatórias.

### Inbox, consumidor SQS e DLQ

- Inbox Message: `UNIQUE (consumer_name, message_id)`, hash do corpo, recebimento e conclusão, gravada na mesma transação SQL das alterações de domínio, ledger e outbox.
- Entrada: `MessageGroupId = walletId`, `MessageDeduplicationId = messageId`. A chave de idempotência é `data.idempotencyKey`.
- Erros permanentes (JSON inválido, envelope desconhecido, validação de formato, OPENING, hash divergente para o mesmo `messageId`) são enviados explicitamente à DLQ e removidos da entrada.
- Erros transitórios: `ChangeMessageVisibility` com backoff exponencial; o redrive leva à DLQ após 5 recebimentos. `VisibilityTimeout=30`, estendido durante processamentos longos.
- Em `SIGTERM`: para o polling, aguarda o processamento em andamento até o prazo e zera a visibilidade das mensagens ainda não concluídas.

### Outbox

- Registro: `eventId` (UUIDv7 gerado no commit), agregado, tipo, versão, payload snapshot, `occurredAt`, tentativas, `next_attempt_at`, `locked_until`, `published_at`.
- Claim com `FOR UPDATE SKIP LOCKED` em registros não publicados com `next_attempt_at` vencido e lease expirado; publica com `MessageGroupId = aggregateId` e `MessageDeduplicationId = eventId`; marca `published_at` em seguida.
- Falha de publicação incrementa tentativas e agenda `next_attempt_at` com backoff exponencial e jitter.
- Métrica de atraso da outbox: idade do registro não publicado mais antigo.

### Worker de Pending References

- Claim com `FOR UPDATE SKIP LOCKED` e lease; cada tentativa reexecuta o caso de uso com a Wallet bloqueada.
- Reference PROCESSED: aplica o Reversal. Reference em PENDING_REFERENCE ou ausente: agenda nova tentativa com backoff. Reference REJECTED ou FAILED: rejeita com `REFERENCE_UNSUCCESSFUL`.
- Esgotado `PENDING_MAX_ATTEMPTS` (padrão 8): REJECTED com `REFERENCE_NOT_FOUND` e `WagerTransactionRejected`.

### Autenticação

- Keycloak com realm `wallet` importado na subida; clients `provider-a`, `provider-b` e `wallet-internal` com `client_credentials`; mapper que injeta o claim `provider_id` nos clients de Provider.
- Validação local de assinatura via JWKS em cache, com verificação de `iss`, `aud`, `exp` e `nbf`.

### Ciclo de vida e configuração

- Configuração por variáveis de ambiente documentadas no README, validadas no start; falha de validação aborta a inicialização.
- Ordem de parada: HTTP para de aceitar conexões, consumidor para o polling, workers terminam ou liberam leases, depois fecham o pool pgx e os clientes AWS.
- IDs internos são UUIDv7.

## Testing Decisions

### O que é um bom teste

Um bom teste observa apenas o comportamento externo: respostas HTTP, mensagens publicadas, mensagens na DLQ e linhas visíveis no banco. Ele sobrevive a refatorações internas. Asserções em chamadas de funções internas, mocks de repositório ou contagem de queries ficam fora. PostgreSQL, Keycloak e MiniStack rodam em containers reais; a infraestrutura nunca é substituída integralmente por mocks.

### Seams

1. **Fronteira do serviço (seam principal).** O serviço é tratado como caixa-preta: entradas por HTTP e `wager-transactions.fifo`; saídas observadas nas respostas HTTP, em `wallet-events.fifo`, na DLQ, nos endpoints de consulta e Reconciliation e, para constraints e imutabilidade, por SQL direto no banco. O mesmo conjunto de cenários roda em dois modos de hospedagem:
   - `-tags=integration`: a aplicação Fx sobe no próprio processo de teste, com banco isolado por execução e filas com sufixo aleatório. Cobre também start e stop da composição Fx e a liberação de recursos dos workers.
   - `-tags=e2e`: o binário compilado sobe em três processos independentes. Cobre a aposta repetida 50 vezes, a disputa 80+80 sobre 100, Wallets distintas em paralelo, HTTP e SQS cruzados para a mesma operação e reinício completo.
2. **API pública do pacote de domínio (seam secundário).** Testes unitários sem infraestrutura para Money (parsing, escala, limites, overflow, moedas incompatíveis, JSON), invariantes da Wallet, transições da Wager Transaction, regras dos cinco tipos externos, política de zero, construção de Ledger Entry, Opening com seus metadados e eventos, Payload Hash e conflito de payload para a mesma chave.
3. **Crashpoints (extensão do seam 1).** Com `-tags=e2e,faultinject`, `CRASH_AT` derruba um processo em `consumer.after_commit`, `outbox.before_publish`, `outbox.after_publish` ou `pending.after_claim`, e o teste confirma que outra instância retoma o trabalho sem duplicar movimentação e preservando o `eventId`.

Casos de uso e repositórios ficam sem seam próprio: HTTP e SQS compartilham o caso de uso, então o seam 1 já os exercita.

### Cenários obrigatórios no seam 1

- Todos os cenários de concorrência e recuperação da seção 13 do enunciado, itens 1 a 8.
- 401 para token ausente, inválido e expirado; 403 para `providerId` divergente e para Provider em endpoint interno; 404 para transação de outro provedor, inclusive em Replay; nenhuma linha financeira criada em acessos negados.
- Reversal antes da Reference com resolução posterior, e com expiração por `PENDING_MAX_ATTEMPTS` reduzido no teste.
- Sequências de Compensation Slot: REFUND duplo, ROLLBACK de REFUND seguido de REFUND, ROLLBACK de ROLLBACK.
- Tentativa direta de `UPDATE` e `DELETE` no ledger falhando no banco.
- Ao final de cada cenário financeiro, Reconciliation consistente e saldo igual a créditos menos débitos.
- Execução com `-race`.

### Prior art

- O teste existente do pacote Money: pacote `_test` externo, tabelas de casos, `errors.Is` contra erros sentinela, apenas a API pública. Os testes de domínio seguem esse formato.
- Para o seam 1 ainda não existe prior art; o primeiro teste de integração estabelece helpers para obter tokens do Keycloak, criar banco e filas isolados e aguardar eventos na fila de saída.

## Out of Scope

- Cadastro de senhas e emissão própria de tokens.
- Reversals parciais.
- Cenários principais em moedas além de BRL; o tipo carrega a moeda e há testes de incompatibilidade.
- Consumidores dos Outbox Events além dos próprios testes.
- Deploy em AWS real, IaC de produção e rotação de segredos.
- Dashboards; tracing com OpenTelemetry e teste de carga com k6 são diferenciais, planejados depois das garantias obrigatórias.
- Interface administrativa e reprocessamento manual da DLQ.

## Further Notes

- Decisões registradas: ADR-0001 (processamento síncrono), ADR-0002 (Compensation Slot), ADR-0003 (lock pessimista com versão). Esta spec segue as três sem contradição.
- Novas decisões desta spec candidatas a ADR: envio explícito à DLQ para erros permanentes, lista de Failure Codes e mapeamento de status HTTP, e claim por lease com `SKIP LOCKED` nos dois workers.
- O `go.mod` declara Go 1.27.1 e o módulo `github.com/junglegaming/backend-challenge-go`; o Dockerfile deve declarar a mesma versão.
- Os testes do pacote Money já existem e ainda falham por falta de implementação; eles são o primeiro passo natural.
- O README e o ARCHITECTURE.md precisam ser revalidados contra o código ao final, removendo os marcadores ⏳.
