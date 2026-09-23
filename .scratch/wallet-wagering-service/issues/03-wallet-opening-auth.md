# 03: Authenticated Wallet opening with zero balance

**What to build:** o serviço interno obtém um token `client_credentials` no Keycloak, abre uma Wallet com saldo inicial zero e a consulta. Provedores e chamadas sem token válido são barrados.

**Blocked by:** 02 (Walking skeleton)

**Status:** ready-for-agent

- [ ] Keycloak no compose com realm `wallet` importado na subida; clients `provider-a`, `provider-b` (com claim `provider_id`) e `wallet-internal`
- [ ] Validação local de JWT via JWKS em cache, verificando `iss`, `aud=wallet-api`, `exp` e `nbf`
- [ ] Token ausente, inválido, expirado ou de outra audience recebe 401
- [ ] Token de Provider em `POST /wallets` e `GET /wallets/:walletId` recebe 403
- [ ] `POST /wallets` com saldo zero cria a Wallet com versão 1, sem Opening, ledger ou eventos
- [ ] Segunda abertura para o mesmo par (jogador, moeda) recebe 409; o schema impõe `UNIQUE (player_id, currency)` e `CHECK (balance >= 0)`
- [ ] Wallet no domínio com criação e reidratação separadas; IDs UUIDv7
- [ ] Corpo de erro único com `code` e `message` estáveis
- [ ] Testes de integração com o Keycloak real cobrindo 401, 403, 201 e 409
