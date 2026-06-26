# iOS Fingerprint Hardening — Findings

Adversarial audit and hardening of the iOS TLS/HTTP-2 fingerprint the app
presents to Cloudflare bot management when replaying a captured iOS-app token.
The current client (bogdanfinn/tls-client) was strong on JA3 but approximate in a
handful of places. This pass closes those gaps. **All verification is offline**
against fingerprint echo services (`tls.peet.ws`) and a cookie/compression echo
(`httpbingo.org` / `httpbin.org`). **No request is made to Deliveroo or
roocdn.com** — the user runs the live dry-run.

Reproduce:

```bash
DELIVEROO_FP_TEST=1 DELIVEROO_TLS_PROFILE=ios18 go test ./internal/deliveroo -run Fingerprint -v   # BEFORE
DELIVEROO_FP_TEST=1 DELIVEROO_TLS_PROFILE=ios26 go test ./internal/deliveroo -run Fingerprint -v   # AFTER (default)
```

## Before / after (captured through the full client against tls.peet.ws/api/all)

| Signal | BEFORE — `Safari_IOS_18_5` | AFTER — `Safari_IOS_26_0` (new default) |
|---|---|---|
| `http_version` | h2 | h2 |
| negotiated proto / ALPN | HTTP/2.0 / h2 | HTTP/2.0 / h2 |
| `ja3_hash` | `773906b0efdefa24a7f2b8eb6985bf37` | `ecdf4f49dd59effc439639da29186671` |
| `ja4` | `t13d2014h2_a09f3c656075_7f0f34a4126d` | `t13d2013h2_a09f3c656075_7f0f34a4126d` |
| `peetprint_hash` | `fdf2c64009327d63a456cbab56a7bdde` | `62b834de729e78a9f0ebd1dd099314a7` |
| supported groups | `29-23-24-25` (X25519,P256,P384,P521) | `4588-29-23-24-25` (adds **X25519MLKEM768**) |
| TLS versions offered | 1.3, 1.2, 1.1, 1.0 | **1.3, 1.2 only** |
| `akamai_fingerprint` | `2:0;3:100;4:2097152;9:1\|10420225\|0\|m,s,a,p` | `2:0;3:100;4:2097152;9:1\|10420225\|0\|m,s,a,p` |
| `akamai_fingerprint_hash` | `c52879e43202aeb92740be6e8c86ea96` | `c52879e43202aeb92740be6e8c86ea96` |

Reading the AFTER row: the TLS layer now matches a current iOS device — the
post-quantum **X25519MLKEM768** key share that iOS 26+/current Safari sends, and
no legacy TLS 1.0/1.1. The HTTP/2 (Akamai) layer is unchanged and already
iOS-shaped: SETTINGS `push=0, max_streams=100, window=2MB, no_RFC7540_priorities`,
connection-flow `10420225`, and pseudo-header order **`m,s,a,p`** =
`:method,:scheme,:authority,:path`.

## Changes

### 1. Default profile → `Safari_IOS_26_0` (`transport.go` `iosProfile`)
The captured device is iOS 27 / iPhone18,4; `Safari_IOS_26_0` is the closest
available (there is no `Safari_IOS_27`). It drops legacy TLS and adds the PQ key
share — both markers of a current device, visible in the table above. Native
NSURLSession shares the OS TLS stack (Apple Secure Transport / Network.framework)
with Safari, so a stock Safari profile is a sound ClientHello proxy. `ios18` and
`ios17` remain selectable via `DELIVEROO_TLS_PROFILE`. We deliberately did **not**
hand-build a custom `ClientProfile`: Charles terminates TLS, so we have no raw
ClientHello from the real device to match against — the maintained profile is a
better proxy than a guess.

### 2. Accept-Encoding fidelity (`client.go`, `transport.go`)
The app sends `Accept-Encoding: gzip, deflate, br`; we previously stripped it.
tls-client transparently decompresses gzip/br/deflate even when the header is set
manually (`DisableCompression` stays false), so we now send the app's exact value
on the tls path and bodies still parse. The header is per-path: the stdlib
fallback (`setIOSAppHeaders`) still strips it, because net/http only auto-decodes
encodings it set itself. `iosHeaderOrder[5]` already slots `accept-encoding`
correctly. **Verified:** `TestAcceptEncodingDecode/gzip` fetches a gzip-compressed
JSON body through the real `doGET` and decodes it cleanly — proof the
Accept-Encoding-sent → transparent-decompress → `json.Unmarshal` path works.

### 3. Explicit pseudo-header order (`transport.go` `doGET`/`doPOST`)
We now set `fhttp.PHeaderOrderKey = :method,:scheme,:authority,:path`. This
matches the Safari_IOS profile's transport default (the akamai fingerprint above
already showed `m,s,a,p`), so it is reinforcement rather than a correction —
it pins the iOS order even if the profile changes.

### 4. Drop stale Cloudflare cookies when seeding (`transport.go` `seedCookies`)
`__cf_bm` is a ~30-minute token bound to the original session/connection;
replaying a stale one is more suspicious than presenting none. We now seed only
`roo_*` (and other non-CF) cookies and skip `__cf_bm` / `cf_clearance` / `__cf*`,
letting Cloudflare mint a fresh `__cf_bm` via Set-Cookie that the jar captures and
resends. **Verified:** `TestCookieRoundTrip` seeds a stale `__cf_bm`, sets a
server cookie via the echo, and confirms (a) the server cookie is captured and
resent by the jar and (b) the stale `__cf_bm` is **not** replayed.

### 5. Optional gated warmup (`client.go` `Warmup`, `transport.go` `doPOST`)
The app POSTs `/consumer/device-fingerprint` (`{"session_id":"<32-hex>"}`) then
`/orderapp/v1/session` (`{}`) at launch. `Warmup(force)` replays these with the
app's **POST header order** (distinct from GETs — `accept` leads, `content-type`
precedes `cookie`) and the iOS pseudo-header order, generating a fresh random
`session_id` via `crypto/rand`. It is gated behind `DELIVEROO_WARMUP=1` (or an
explicit `force`), is **never auto-called and never set in tests**, and `doPOST`
refuses the stdlib fallback. It is wired to nothing by default — opt-in only, for
the user's own live dry-run. It may establish session trust + a fresh `__cf_bm`
before the order pull.

### 6. Offline harness (`fingerprint_test.go`, gated `DELIVEROO_FP_TEST=1`)
Replaced the single bare echo with a suite routed through a real `NewClient()` so
it exercises the same header order, pseudo-header order, Accept-Encoding, and jar
as the live path: a fingerprint dump (JA3/JA4/peetprint/akamai + negotiated
proto/ALPN), a cookie round-trip, and per-codec decode subtests. All network hits
stay behind the gate; the ungated `go test -race ./...` is unaffected.

## What was already correct (verified, unchanged)
- **Header order** — the 12-entry `iosHeaderOrder` matches the capture's GET order
  exactly (minus `Connection`, correctly dropped for h2).
- **Connection reuse** — one tls-client instance, cached transports, h2
  multiplexing, 90s idle keep-alive: one handshake, multiplexed streams (not a
  fresh JA3 per request).
- **ALPN** `h2,http/1.1`, **SNI** present, **GREASE** in ciphers/curves/versions/
  key share — all iOS-like.

## Residual risks (honest)
1. **HTTP/1.1 vs HTTP/2 (top item).** The Charles captures display as HTTP/1.1
   with `Connection: keep-alive` — almost certainly a Charles MITM downgrade
   artifact (Charles negotiates h1.1 with the app unless "Enable HTTP/2" is on),
   since a modern iOS app against a Cloudflare h2 endpoint negotiates h2 via ALPN.
   We design for h2 (correct for prod) and the harness confirms `proto=HTTP/2.0,
   alpn=h2`. **Action for the user:** re-capture with Charles HTTP/2 enabled to
   confirm the real app is h2 on the wire; if it is genuinely h1.1, the akamai
   layer is moot and we'd force h1.1 instead.
2. **Safari vs native NSURLSession at the h2 layer.** tls-client only has Safari
   iOS profiles. JA3/JA4 (TLS) match the OS stack, but NSURLSession's h2 SETTINGS
   / window could differ subtly from Safari's. The akamai fingerprint is a close
   proxy, not provably identical to the native app, and we can't capture the
   app's raw h2 SETTINGS through Charles.
3. **UA vs TLS version mismatch.** The captured UA says iOS 27; the TLS reads
   iOS 26 (no `Safari_IOS_27` exists). A sophisticated detector cross-referencing
   UA against TLS version could notice. Lowest-risk option available today.
4. **No raw ClientHello to match.** Charles terminates TLS, so we cannot diff the
   real device's ClientHello byte-for-byte; we trust the maintained profile equals
   a real iOS 26 device. If the user later captures a raw ClientHello (pcap +
   Wireshark JA4, or a non-MITM TLS fingerprint service the phone hits), we could
   build a precise custom profile.
5. **If-Modified-Since deviation.** The app sends `If-Modified-Since` on order
   *detail* GETs; we strip it to avoid a 304 with an empty body. The header's
   presence is a minor fidelity gap. A future tweak could send it with an epoch
   date (forces a 200 while keeping the header); not done here to keep behavior
   predictable.
6. **brotli/deflate not green at capture time.** `gzip` decode is verified green
   through the real `doGET` path. `brotli` (the app's actual codec) and `deflate`
   were intermittently unreachable on the public echoes (httpbin.org 503,
   httpbingo.org `/brotli` 501 / `/deflate` timeout) and were skipped, not failed.
   They ride the **identical** `Content-Encoding` dispatch in fhttp (gzip/br/
   deflate/zstd readers selected by the response header), so the gzip proof
   transfers; the brotli reader is `andybalholm/brotli`, already wired in.
7. **Behavioral / token binding.** The replayed Authorization + `roo_*` cookies
   are bound to the user's account; uniform 6–20s pacing is reasonable but not a
   perfect human model. The first request now carries no `__cf_bm` (by design),
   which is authentic for a fresh session but is the change most likely to alter
   live outcomes — validate against the real host first.
