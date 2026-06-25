# CLAUDE.md

Guidance for Claude Code when working in this repo.

## Build & run

```bash
go build -o server ./cmd/server
./server                                  # port 8080, data in ~/.deliveroostats
DELIVEROO_SEED=1 ./server                 # demo with synthetic orders
PORT=3000 DELIVEROO_DATA_DIR=/tmp/d ./server
```

Run `gofmt -l .` and `go vet ./...` before every commit (CI gates Lint on gofmt).

## Architecture

Deliveroo Wrapped is a Go web app visualizing Deliveroo order history in a
"Spotify Wrapped" style. It is a domain port of `../lime-wrapped` (rides → orders,
vehicles → restaurants/drivers). Single binary, embedded templates + static
assets, HTMX, Chart.js, Leaflet, local JSON storage.

### Components

- `cmd/server/main.go` — HTTP server, `Server` struct, inline `funcMap`, routes:
  pages (`/`, `/auth`) and HTMX/JSON endpoints (`/api/manual-auth`, `/api/sync`,
  `/api/sync-status`, `/api/stats`, `/api/order-locations`, `/api/logout`).
- `cmd/server/seed.go` — synthetic dev data behind `DELIVEROO_SEED=1`.
- `internal/deliveroo` — `client.go` (throttled token-replay API client; replays
  the captured iOS-app header block verbatim via `setIOSAppHeaders`) and
  `curl.go` (parses a "Copy as cURL" paste into token + headers).
- `internal/models` — Deliveroo API response types (**`TODO(phase0)`**: align
  with real captured payloads) and our own flattened `StoredOrder` + `YearlyStats`.
- `internal/storage` — JSON persistence to `~/.deliveroostats/` and the
  `AddOrderFromDetail` adapter (API → `StoredOrder`).
- `internal/stats` — `Calculate()` plus `CalculateRestaurantStats`,
  `CalculateDriverStats` (repeat-driver detection), `GetStreakDays`.

### Data flow

1. User pastes a captured iOS-app request at `/auth` → token + headers saved.
2. Sync paginates order history (throttled), fetches each order's detail,
   flattens to `StoredOrder`, persists to `deliveroo_data.json`.
3. Stats computed on-demand from stored orders, filtered by `?year=YYYY`/`all`.
4. Templates render tiles + Chart.js charts + a Leaflet delivery map.

### Key invariant

`StoredOrder` is the stable contract. Deliveroo's payload shape only touches
`internal/models` API types and `storage.AddOrderFromDetail`. Don't couple stats
or templates to the raw API shape.

## Phase 0 (before the live path works)

Capture real order-history + order-detail payloads off the phone (see
`docs/SPEC.md`), save to `docs/api-samples/` (gitignored), then adjust the API
types' json tags / nesting and the base URL + pagination in `client.go`.
