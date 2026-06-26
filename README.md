# Deliveroo Wrapped

Your personal year-in-review for Deliveroo orders — a Spotify Wrapped-style
dashboard of spend, fees, Plus savings, delivery times, top restaurants, tips,
cuisines and more. Local-only; nothing leaves your machine.

Ported from [`lime-wrapped`](../lime-wrapped). See [`docs/SPEC.md`](docs/SPEC.md)
for the full product spec.

## Quick start

```bash
go build -o server ./cmd/server

# Demo with synthetic data (no Deliveroo account needed):
DELIVEROO_SEED=1 ./server          # http://localhost:8080

# Real data:
./server                           # then open /auth and paste a captured request
```

## Getting your data (token replay)

Deliveroo has no public API, so we replay a request captured from the iOS app:

1. Run a TLS proxy on your phone (Proxyman / mitmproxy / Charles) with its cert trusted.
2. Open Deliveroo → **Order history**.
3. Find the order-history request, **Copy as cURL**, and paste it at `/auth`.
4. Click **Sync** to fetch your orders (throttled to look like the app).

Re-syncs only need a fresh token — captured headers are remembered.

> ⚠️ The capture contains your auth token and personal data. It is stored only in
> your local data dir and `docs/api-samples/` is gitignored. Never commit it.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `DELIVEROO_DATA_DIR` | `~/.deliveroostats` | Where order data + auth are stored |
| `DELIVEROO_PLUS_MONTHLY` | `3.49` | Plus subscription price/month (for ROI) |
| `DELIVEROO_SEED` | – | Set to `1` to load demo data when no real data exists |

## Status

Dashboard MVP. The live data path has `TODO(phase0)` seams in
`internal/models` + `internal/storage` that are filled in once real Deliveroo
order-history / order-detail payloads are captured (see `docs/SPEC.md`). The
dashboard, stats engine, charts, map, auth/cURL parsing and sync pipeline are all
built and verified against seed data.

Built with Claude Code.
