# 0001 — O api-gateway é a borda da rede, não o gateway de pagamento

- Status: aceita
- Data: 2026-10-21

## Contexto

O nome "gateway" aparece em dois lugares do sistema. O gateway de **pagamento** é o PSP próprio, em `payment-ledger-service`. Este projeto é o gateway de **API**: a única porta de entrada HTTP para a loja e para o PSP.

## Decisão

O api-gateway faz o roteamento e as preocupações transversais. Não tem regra de negócio, não valida JWT nem chave de API e não guarda estado.

| Prefixo | Destino |
|---|---|
| `/api/*` | e-commerce-RF (loja) |
| `/v1/*` | payment-ledger-service (API pública do PSP, chaves `sk_`/`pk_`) |
| `/rails/*` | payment-ledger-service (callbacks dos trilhos) |
| `/simulator/*` | payment-ledger-service (simuladores, só em dev) |

Atualização de 2026-10-21: o navegador passa a falar com o PSP pelo prefixo `/psp`, que deixa passar **apenas** `POST /psp/v1/tokens` (tokenização de cartão, ADR 0005 do PSP) e `/psp/simulator/*` (com o simulador ligado). O restante (`/*`) vai para o front da loja quando `WEB_URL` está configurada.

Preocupações transversais: TLS, CORS, `X-Request-Id`, limite de requisições (por chave de API em `/v1`), timeouts e logs de acesso.

## Consequências

- Os serviços confiam no `X-Request-Id` para correlacionar logs, mas continuam autenticando por conta própria.
- O PSP e a loja não ficam expostos diretamente; só o api-gateway publica porta.
