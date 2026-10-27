# api-gateway

Porta de entrada HTTP da Loja Prateleira: roteia o tráfego público para a loja (`e-commerce-RF`) e para o PSP (`payment-ledger-service`). Não tem regra de negócio e não autentica ninguém; cada serviço confere as próprias credenciais. A decisão está em [`docs/adr/0001`](docs/adr/0001-api-gateway-na-borda.md).

## Rotas

| Prefixo | Destino | Observação |
|---|---|---|
| `/api/*` | e-commerce | o prefixo sai: `/api/products` chega como `/products` |
| `/v1/*` | PSP | API dos lojistas, com limite de requisições por chave de API |
| `/rails/*` | PSP | avisos dos trilhos de pagamento (bancos) |
| `/simulator/*` | PSP | só com `SIMULATORS_ENABLED=true`; nunca em produção |
| `POST /psp/v1/tokens` | PSP (`/v1/tokens`) | o navegador tokeniza cartões; mesmo limite de `/v1` |
| `/psp/simulator/*` | PSP (`/simulator/*`) | o botão "Simular pagamento" do front; só com o simulador ligado |
| `/*` (o resto) | front da loja | só com `WEB_URL`; sem ela, `404` |
| `/healthz` | o próprio gateway | `{"status":"ok"}` |

As rotas de operador do PSP (`/platform/*` e `/ledger/*`) nunca chegam a ele, nem pelo prefixo `/psp`. Sem `WEB_URL` elas dão `404` JSON; com o front configurado, quem responde é o `index.html` dele (fallback do React Router), sem nenhum dado do PSP.

O navegador fala com o PSP sempre pelo prefixo `/psp`, como faz o proxy do Vite em desenvolvimento. Por esse prefixo passam só a tokenização de cartão e o simulador.

O webhook do PSP para a loja (`POST /webhooks/payments`) é tráfego interno, de serviço para serviço, e também não passa pelo gateway.

## O que o gateway faz em toda requisição

- **`X-Request-Id`**: mantém o id recebido, se for seguro registrar, ou gera um novo. O id vai para o serviço e volta na resposta.
- **Log de acesso** em JSON, uma linha por requisição, com método, caminho, status, duração e request id. Cabeçalhos e query string não são registrados, porque carregam chaves de API e JWTs.
- **Limite de requisições** em `/v1/*`: um token bucket por chave de API, ou por IP em chamadas sem chave. Ao estourar, responde `429` com `Retry-After`. As chaves ficam na memória só como hash.
- **Corpo** limitado a 1 MiB (`413`).
- **Timeouts**: 5 s para ler os cabeçalhos, 15 s para ler a requisição e 25 s esperando o serviço. Serviço fora do ar vira `502` em JSON.
- **CORS** só para as origens de `CORS_ALLOWED_ORIGINS`.
- **Desligamento gracioso** no `SIGTERM`, deixando as requisições em andamento terminarem.

## Configuração

| Variável | Padrão |
|---|---|
| `PORT` | `8080` |
| `ECOMMERCE_URL` | `http://localhost:3000` |
| `PSP_URL` | `http://localhost:3001` |
| `WEB_URL` | vazio (sem front) |
| `SIMULATORS_ENABLED` | `false` |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `10` / `20` |
| `CORS_ALLOWED_ORIGINS` | vazio (separadas por vírgula) |

## O sistema inteiro: `docker compose up`

O [`docker-compose.yml`](docker-compose.yml) sobe a Loja Prateleira completa atrás do gateway, supondo os três repositórios lado a lado (`../e-commerce-RF`, `../payment-ledger-service`):

```bash
docker compose up --build      # http://localhost:8080
```

| Serviço | O que faz |
|---|---|
| `psp-db`, `ecommerce-db` | Postgres de cada serviço (sem porta publicada) |
| `psp` | aplica as migrations e sobe o PSP com os trilhos simulados |
| `setup` | uma vez só: cadastra a loja como lojista no PSP, registra o webhook e grava as chaves num volume compartilhado; nas próximas subidas, não faz nada |
| `ecommerce` | lê as chaves, cria o schema (a loja ainda não tem migrations), semeia e sobe |
| `web` | o front, servido por nginx, com o "Simular pagamento" ligado |
| `gateway` | a única porta publicada (8080) |

Para entrar, use `cliente@prateleira.com` ou `admin@prateleira.com`, com senha `123456`. Para testar o pagamento:
- o Pix é pago pelo botão "Simular pagamento";
- o cartão `4242 4242 4242 4242` aprova;
- o Financeiro do admin mostra o saldo, que o PSP libera a cada 10 s nesse ambiente.

Os segredos do compose são de desenvolvimento e podem ser sobrescritos por variáveis de ambiente: `PLATFORM_ADMIN_TOKEN`, `VAULT_ENCRYPTION_KEY` e `SECRET_KEY_JWT`. Para recomeçar do zero, use `docker compose down -v`.

## Rodando só o gateway

Sem Go instalado, tudo funciona pelo Docker:

```bash
docker build -t api-gateway .
docker run --rm --network host -e SIMULATORS_ENABLED=true api-gateway

# testes
docker run --rm -v "$PWD":/src -w /src -e CGO_ENABLED=0 golang:1.22-alpine go test ./...
```

Com Go 1.22 ou mais novo: `go run .` e `go test ./...`.

O limite de requisições é por instância. Com várias réplicas, cada uma conta separado; um store compartilhado (Redis) é o passo seguinte se isso importar.
