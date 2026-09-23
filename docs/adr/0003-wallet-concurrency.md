# Lock pessimista por linha de carteira com verificação de versão

Escritores de uma mesma carteira são serializados por `SELECT ... FOR UPDATE` na linha da Wallet dentro da transação; o `UPDATE` ainda exige `version = $esperada` e o schema impõe `CHECK (balance >= 0)`. Rejeitamos controle apenas otimista porque a disputa em carteiras quentes (várias apostas simultâneas) geraria retries e latência imprevisível; o lock é por linha, então carteiras distintas seguem em paralelo e não há lock global.
