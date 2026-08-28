# Gatekeeper

[![CI](https://github.com/EkimBolat/gatekeeper/actions/workflows/ci.yml/badge.svg)](https://github.com/EkimBolat/gatekeeper/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

Traffic-control infrastructure for high-demand on-sales — the piece that sits in front of a ticket-selling site, not the site itself. Redis-backed seat locking plus a virtual waiting room guarantee no seat ever sells twice, even under thousands of concurrent requests. (Inspired by Ticketmaster's 2022 Taylor Swift on-sale crash.)

**[Try the live demo](https://ekimbolat.github.io/gatekeeper/demo/seat-map.html)** — join a queue, get admitted, pick a seat, check out, against 5 real services on Render. (First request may take ~50s — free instances sleep when idle.)

![Live seat map demo](./demo/screenshot-v2.png)

## Highlights

- **Concurrency-safe locking** — Redis `SETNX` + TTL; exactly one winner per seat, proven with 50 local goroutines and 100 real HTTP clients against the live deployment (see [Testing](#testing)).
- **Purchase saga** with compensating rollback on failure.
- **Enforced access control** — admission tokens (waiting room → lock) and a shared internal secret (order → confirm/charge/refund) are each verified independently by every service that needs them, not just the gateway.
- **Live seat map** over WebSocket, a multi-event catalog, and deployed for real on Render.

## Architecture

Six small services, each owning its own piece — full request flow in [ARCHITECTURE.md](./ARCHITECTURE.md).

| Service | Responsibility |
|---|---|
| **api-gateway** | Routes requests, rate-limits by IP, enforces admission tokens |
| **waiting-room** | Virtual queue, admits in batches, issues admission tokens |
| **seat-locking** | Redis-backed seat lock, live status over WebSocket |
| **order** | Purchase saga: charge → confirm → compensate on failure |
| **payment** | Mock processor, idempotent per order |
| **notification** | Consumes order events off RabbitMQ |

## Tech stack

Go · Redis · RabbitMQ · PostgreSQL · WebSocket · Docker Compose

## Getting started

```bash
git clone https://github.com/EkimBolat/gatekeeper.git
cd gatekeeper
docker-compose up --build
```

Each service exposes `/health` (ports 8080–8085; RabbitMQ UI on 15672, guest/guest). `demo/seat-map.html` points at the hosted Render backend by default — change `GATEWAY_URL` in the `<script>` to run it against your local stack.

Open it in two tabs and race for the same seat to watch the lock resolve live.

## Testing

```bash
cd services/seat-locking && go test -tags=integration ./... -v
cd services/order && go test -tags=integration ./... -v
```

`TestLockSeat_ConcurrentCallers_OnlyOneWins` fires 50 concurrent goroutines at one seat — exactly one wins. `scripts/loadtest` proves it again over real HTTP against the live deployment: 100 clients, 1 winner, 0 errors.

## Status

All 6 services work end to end, including the full purchase saga. Concurrency, saga, and auth behavior are covered by unit, integration, and live load tests.

## License

MIT — see [LICENSE](./LICENSE).
