# Deliveroo Wrapped — Product Spec

A personal, local-only "year in review" for your Deliveroo order history, in the
style of Spotify Wrapped. Ported from `lime-wrapped`. Single Go binary, HTMX
front-end, Chart.js + Leaflet, data stored as local JSON. **Nothing leaves your
machine.**

## Headline questions (the build answers all of these)

| Question | Stat field(s) | Surface |
|---|---|---|
| How much did Plus save me on delivery + service fees? | `TotalPlusSavings`, `PlusDeliverySaved`, `PlusServiceSaved` | "Saved with Plus" hero tile |
| Was Plus worth the subscription? (cost analysis / ROI) | `PlusSubscriptionCost`, `PlusROI`, `PlusNetBenefit` | "Was Plus worth it?" section |
| How much did I spend at my top restaurants? | `TopRestaurants[]` (count + spend) | Top-restaurants leaderboard |
| Average / longest / shortest delivery time | `AvgDeliveryMinutes`, `LongestDelivery*`, `ShortestDelivery*` | Delivery-times grid |
| How much in tips? | `TotalTips`, `AvgTip`, `TippedOrderPct` | "Total tips" tile |
| Average order cost | `AvgOrderTotal` | "Total spent" tile sub |
| Day with the most orders | `MostOrdersInOneDay`, `MostOrdersDate` | "Busiest day" tile |
| Did I ever get the same driver? | `DriverDataAvailable`, `UniqueDrivers`, `RepeatDriverOrders`, `TopDriver` | "Your drivers" section |

## Spec extras (chosen, all shipped in MVP)

- **Cuisine & dish breakdown** — `OrdersByCuisine`, `SpendByCuisine`, `TopDishes[]`
  (doughnut chart + dishes table).
- **Fee economics** — `TotalFees`, fee-to-food ratio via `FeesAsMeals`
  ("your fees ≈ N extra meals"), Plus ROI. The TfL-comparison analog from
  lime-wrapped.
- **Habits & timing** — `LateNightOrders` (9pm–4am), `WeekdayOrders`/`WeekendOrders`,
  `BusiestMonth`, longest ordering streak (`GetStreakDays`), spend-by-month and
  orders-by-day-of-week charts.
- **Delivery map** — Leaflet map of every restaurant + delivery location
  (`/api/order-locations`).

## Data acquisition (token replay)

Deliveroo has no public API and uses short-lived, app-issued bearer tokens behind
bot protection. We replay a request captured from the **iOS app**. Capture tool:
**Charles Proxy** (on the Mac).

### Charles capture walkthrough
1. Charles → **Proxy → SSL Proxying Settings** → enable SSL Proxying and add a
   location `*.deliveroo.com : 443` (the host may turn out to be `api.deliveroo.com`
   or similar — widen to `*:443` if unsure, then narrow once seen).
2. Charles → **Help → SSL Proxying → Install Charles Root Certificate on a Mobile
   Device or Remote Browser** — note the proxy host (your Mac's LAN IP) and port
   (default **8888**).
3. On the iPhone: **Settings → Wi-Fi → (i) → Configure Proxy → Manual**, set
   server = Mac IP, port = 8888. Then visit **chls.pro/ssl** in Safari to install
   the cert profile, **Settings → General → VPN & Device Management** to install
   it, and **Settings → General → About → Certificate Trust Settings** to enable
   **full trust** for the Charles cert.
4. Open Deliveroo → **Order history** (open a past order too). In Charles, find
   the `deliveroo.com` order-history / order-detail requests.
5. Right-click the request → **Copy cURL Request**. Paste into `/auth`.
6. The app extracts the `Authorization` token and keeps **every other header
   verbatim** (`User-Agent`, `x-roo-*`, client/version, device IDs) so requests
   match the real app fingerprint. We deliberately do **not** send browser
   `Sec-Ch-Ua` / `Sec-Fetch-*` headers.
7. Sync paginates the order-history endpoint (throttled 0.8–2.5s) and fetches
   each order's detail. Re-sync only needs a fresh token (headers persist).

> **TLS pinning caveat:** if the Deliveroo app pins certificates, Charles will
> show SSL handshake failures / unreadable bodies for its requests even with the
> cert trusted. Workarounds: try an older app build, use a jailbroken device with
> SSL Kill Switch 2, or fall back to the **paste-raw-JSON** path (save the decoded
> response bodies Charles *does* capture, or export via another channel, and parse
> them directly — a fast-follow if live replay is blocked).

### Phase 0 — capture checklist (blocks the live path)
Save real samples to `docs/api-samples/` (gitignored), then align
`internal/models` API types + `storage.AddOrderFromDetail`:
- Order-history list: URL, method, pagination (cursor vs page), one full page.
- Order detail: fee breakdown, tip, timestamps, restaurant + delivery coords,
  rider block, line items.
- Confirm whether **Plus savings** are explicit fields or implicit (fee shown as
  £0). If implicit, derive savings from a standard-fee baseline minus charged fee.
- Confirm whether a **stable driver ID** exists; if not, "same driver" falls back
  to name match or shows "unavailable" (already handled).

## Real API contract (captured 2026-06, UK app v3.328.0)

Host `co-m.uk.deliveroo.com`, auth `Authorization: Basic base64(userId:orderapp_ios,<JWT>)`.

| Endpoint | Purpose |
|---|---|
| `GET /orderapp/v1/users/{userId}` | profile: name, Plus tier (`DIAMOND`), `offer_uname` (→ price) |
| `GET /consumer/order-history/v1/orders?limit=25&offset=N&include_ugc=true` | order history (full per-order data), offset/limit paging |
| `GET /orderapp/v1/users/{userId}/orders/{orderId}` | per-order detail (service-fee breakdown, etc.) — not used by MVP |

`userId` is decoded from the Basic credential. Money + timestamps are JSON
**strings** (sometimes `""`) and are parsed in storage.

### List vs detail, and enrichment
The order-history list is rich (money, items, restaurant name, delivery coords,
status, timestamps) but does **not** include service fees (`fee` empty),
restaurant coordinates (`[0,0]`), or driver identity (`drivers` empty even for
delivered orders). The per-order **detail** endpoint supplies the service fee
(`fee`, ~£0.99 flat in samples) and real restaurant coordinates (`[lng,lat]`).

**Sync therefore runs in two phases:** list ingest (~35 calls) then enrichment —
one detail call per delivered, not-yet-enriched order. Enrichment is incremental
(only un-enriched orders) and **resumable**: on a 401/403 (expired token /
Cloudflare cookie) it saves progress and stops; re-paste a fresh token and Sync
(or "Enrich") resumes. The restaurant heatmap uses enriched coordinates, falling
back to delivery locations until enriched.

Still unavailable from any captured endpoint:
- **Cuisine** (`category` empty in list; absent from detail) — cuisine breakdown
  stays blank, hidden when empty.
- **Driver identity** — "same driver?" shows "not available" (it likely lives in
  the live order-tracking endpoint, not order history).

## Architecture

```
cmd/server/        main.go (handlers, funcMap), seed.go (dev data), templates/, static/
internal/deliveroo client.go (throttled token-replay client), curl.go (Copy-as-cURL parser)
internal/models    API response types (TODO phase0) + StoredOrder + YearlyStats
internal/storage   local JSON persistence + API→StoredOrder adapter
internal/stats     Calculate() + restaurant/driver leaderboards + streaks
```

- `StoredOrder` is our own flattened type; everything downstream depends on it,
  not on Deliveroo's payload, so only the API types + adapter change post-capture.
- Year filtering via `?year=YYYY` / `?year=all` throughout.

## Kinetic redesign (Direction A)
The whole UI is a Spotify-Wrapped-grade "Kinetic" redesign (from a Claude Design
handoff): hub (`/`), a 9-scene scroll **story** (`/story`, count-ups + confetti),
the **explore** dashboard (`/explore`, Chart.js bar + radar, Leaflet delivery
heatmap, leaderboards, YoY delta chips, records), and **share cards** (`/cards`,
square 1080² + story 9:16). Year via `?year=`. Design system in
`static/css/wrapped.css` + `static/js/wrapped.js`; fonts Bricolage/Hanken;
`prefers-reduced-motion` honored. Backend additions for it: `Stats.TopAddresses`
(delivery-address aggregate w/ centroid coords + pct), `DataStore.MemberSince`,
and funcMap helpers (`monogram`/`restColor`/`dayMonth`/`divf`). Only Direction A
was delivered; an editorial Direction B could be added later.

## Shipped beyond the original asks
From a deeper pass over the order payloads (no extra API calls): **Deliveroo
beat-its-ETA %** (delivered vs estimated), **home-vs-office split**
(`address.label`), **power hour / peak time**, **credits & refunds used**
(`credit_used`), **top customisations** (item `modifiers`), and **restaurant
logos** in the leaderboard (cached to disk, served from `/api/logo?r=ID`, fetched
once with the iOS-app UA — never from the CDN directly in the UI).

## Future: reservations auto-booker (separate feature)
Deliveroo now offers restaurant **reservations** (endpoint
`GET /api/reservations/bookings` already seen in capture). Idea: monitor slot
availability at chosen restaurants, poll for newly-released slots, and
**auto-book** the first match based on user prefs + settings, optionally synced
with the user's calendar to only book when free. Needs: reservation
search/availability endpoints (capture), a booking POST (capture + careful, it's
a real write/commit action — must be gated behind explicit confirmation/limits),
a poller (K8s CronJob or local ticker), and calendar integration.

## Out of scope (fast-follow backlog)

- **Share cards** — social-shareable PNG/cards (lime-wrapped `share.html`).
- **Per-order detail page**.
- **Multi-year compare** — "vs last year" deltas.
- **Cuisine diversity score**, dietary mix, new-vs-regular restaurant split.
- **Full login flow** (auto token refresh) instead of manual paste.
- Subresource Integrity (SRI) hashes on CDN scripts (htmx/Chart.js/Leaflet).
