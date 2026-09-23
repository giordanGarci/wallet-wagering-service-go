# Processamento síncrono em uma única transação SQL

Operações sem dependência pendente (BET, WIN, LOSS e Reversals com Reference disponível) são validadas, aplicadas e concluídas em uma única transação SQL, sem commit intermediário de `PENDING`. Apenas `PENDING_REFERENCE` é persistido como estado intermediário e retomado por um worker. Isso elimina a classe de falha "PENDING confirmado e abandonado" e mantém o replay trivial; o custo é que a requisição HTTP segura o lock da carteira durante o processamento.
