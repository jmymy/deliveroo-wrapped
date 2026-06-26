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

### UI — "Kinetic Wrapped" (Direction A)

The front-end is a Spotify-Wrapped-style experience (ported from a Claude Design
handoff). Pages: a hub (`/`), a 9-scene scroll story (`/story`), the explore
dashboard (`/explore`), social share cards (`/cards`), an empty state, and the
connect form (`/auth`). `/share` 301-redirects to `/cards`. Year is a `?year=`
query param (server re-renders; `all` = all-time). Design system lives in
`cmd/server/static/css/wrapped.css` + `static/js/wrapped.js` (count-ups, scroll
reveals, story engine, confetti, parallax — all DOM/class driven, respects
`prefers-reduced-motion`). Fonts: Bricolage Grotesque + Hanken Grotesk (Google
Fonts). Charts via Chart.js, map/heatmap via Leaflet + leaflet.heat (CDN + SRI).

### Components

- `cmd/server/main.go` — HTTP server, `Server` struct, inline `funcMap`, the
  `buildPageCtx`/`baseData`/`buildViewModel` helpers, page handlers
  (`handleHub`/`handleStory`/`handleExplore`/`handleCards`/`handleAuth`) and
  HTMX/JSON endpoints (`/api/manual-auth`, `/api/sync`, `/api/enrich`,
  `/api/sync-status`, `/api/stats`, `/api/order-locations`, `/api/logo`,
  `/api/logout`). Page handlers render the new templates; missing-data falls to
  `empty.html`. `explore.html` gets a `ViewModel` JSON (spend-by-month,
  orders-by-day Mon-Sun, destinations) injected as `const ROO`.
- `cmd/server/seed.go` — synthetic dev data behind `DELIVEROO_SEED=1` (two
  delivery addresses so the dest-split + heatmap render).
- `internal/deliveroo` — `client.go` (throttled token-replay API client; replays
  the captured iOS-app header block), `curl.go` (parses a "Copy as cURL" paste),
  and `transport.go` (the iOS-fingerprinted HTTP client via `bogdanfinn/tls-client`
  — JA3/JA4 + HTTP/2 + header + pseudo-header order matching an iPhone; sends the
  app's `Accept-Encoding: gzip, deflate, br` (tls-client auto-decompresses) and
  seeds only `roo_*` cookies so Cloudflare mints a fresh `__cf_bm`; used for all
  API calls so the bot-protected detail-endpoint pull isn't flagged as Go).
  `Client.Warmup(force)` optionally replays the app's launch POSTs
  (`/consumer/device-fingerprint`, `/orderapp/v1/session`), gated behind
  `DELIVEROO_WARMUP=1` and never auto-called. `fingerprint_test.go` is a gated
  (`DELIVEROO_FP_TEST=1`) offline echo suite (JA3/JA4/akamai + cookie + codec
  checks; see `FINDINGS.md`). Enrichment env: `DELIVEROO_TLS_PROFILE`
  (ios26 default | ios18 | ios17), `DELIVEROO_ENRICH_BATCH` (per-session cap, default 30).
  Enrichment is manual/capped/block-safe (see `runEnrichment`); Sync/Dry-run/Enrich
  buttons live on `/auth`.
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
