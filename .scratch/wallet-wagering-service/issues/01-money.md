# 01: Money value object

**What to build:** o value object Money exato (int64 em unidades mínimas, escala fixa de duas casas, moeda ISO 4217) e Currency, conforme a seção Money da spec. Os testes existentes do pacote Money definem a interface pública e passam a verde.

**Blocked by:** None (can start immediately)

**Status:** done

- [x] Todos os testes existentes do pacote Money passam com `go test -race`
- [x] Parsing aceita apenas a forma canônica e rejeita vazio, `NaN`, `Infinity`, notação científica, escala excedente, sinais e overflow com `ErrInvalidAmount`
- [x] Moedas fora da lista ISO 4217 conhecida retornam `ErrInvalidCurrency`
- [x] Soma, subtração, negação e comparação detectam overflow (`ErrOverflow`) e moedas incompatíveis (`ErrCurrencyMismatch`)
- [x] O valor zero de Money e de Currency é inválido e retorna `ErrUninitialized` em qualquer operação
- [x] JSON estrito `{"amount":"25.00","currency":"BRL"}` em ambas as direções; números JSON, `null`, campos ausentes e extras são rejeitados
- [x] Nenhum `float32`/`float64` no pacote
- [x] O pacote não importa Fx, HTTP, SQS nem pgx
