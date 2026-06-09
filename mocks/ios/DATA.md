# duck for iOS — data availability map

What the app can pull today, what's one unwrap away, and what needs building.
Sources: `cassandra-schwab-broker`, `cassandra-schwab-mcp`, `cassandra-market-research`, `cassandra-portal`, `flock-server`.

## TL;DR — UI element → source → status

| UI element | Source | Status |
|---|---|---|
| Positions list (qty, mkt value, avg price) | broker `GET /users/{email}/positions` | ✅ works today |
| Account value, cash | broker `GET /users/{email}/accounts` | ✅ works today |
| Quote per symbol (mark, day chg %, range, volume) | broker `GET /users/{email}/quote/{symbol}` | ✅ works today |
| **Per-position day P/L** | Schwab sends `currentDayProfitLoss` — broker normalizer **drops it** | 🔧 unwrap (add fields in `schwab_client.py:_normalize_accounts`, lines ~198) |
| Buying power, account day P/L | also dropped in normalization | 🔧 unwrap |
| **Today feed: fills, dividends, transfers** | Schwab `GET /trader/v1/accounts/{hash}/transactions` (60-day window) — in schwab-py, not wrapped | 🔧 wrap new broker endpoint |
| **Working/filled order status** | Schwab orders API (`get_orders`) — in schwab-py, not wrapped | 🔧 wrap |
| **Place/preview/cancel orders** | schwab-py `place_order()`/`preview_order()`/`cancel_order()` (base.py:307-337) — zero integration today, broker is read-only | 🏗 build (broker endpoints + approvals gate) |
| Intraday chart (1D sparkline) | market-research `intraday_prices` (1m–4h candles, 5m adaptive, ≤7 days back) — or unwrap Schwab `pricehistory` | ✅ MCP today / 🔧 broker |
| Longer charts (1W…ALL) | market-research `price_history` (5y OHLC + SMA/volatility) | ✅ today |
| News per holding | market-research `market_news(symbol, limit)` | ✅ today |
| Earnings badge ("NVDA reports Thu") | market-research `earnings_calendar` (FMP + TradingView fusion, EPS est) | ✅ today |
| Sector/market context | market-research `sector_performance`, `index_performance`, `market_overview` | ✅ today |
| Market open/closed state | market-research `market_hours` (or unwrap Schwab markets) | ✅ today |
| Real-time push (ticks, fill alerts) | schwab-py `StreamClient` WebSocket — not integrated | 🏗 build (later; polling is fine for v1) |
| Sessions / chat / automations | flock-server REST + WS on `:8787` (`/runs`, `/runs/{id}/chat`, `/runs/{id}/stream`) | ✅ today (over Tailscale) |
| Trade approval queue | nothing exists — flock currently runs `approvalPolicy: never` | 🏗 build (`approvals-gate`) |

## "What changed today" — the recipe

The Today feed in the mock composes from four feeds, in order of effort:

1. **Day P/L deltas** — unwrap `currentDayProfitLoss`, `currentDayProfitLossPercentage`, `longOpenProfitLoss` per position and `buyingPower`/day-P/L at account level. ~10 lines in `schwab_client.py`.
2. **Transactions** — new broker endpoint `GET /users/{email}/transactions?days=1` wrapping schwab-py's transactions call. Gives fills, dividends, interest, transfers with timestamps — the backbone of the feed.
3. **Orders** — `GET /users/{email}/orders?from=today` for working/filled/cancelled order states (what the swipe-approved trades show while pending).
4. **Agent events** — flock run completions/flags already stream over the `:8787` WebSocket; the gate's proposals/decisions are app-local.

Snapshot diffing (poll positions every N min, diff against morning snapshot) is a zero-backend fallback, but with 1–3 above it's unnecessary.

## Broker (`cassandra-schwab-broker`) — exists today

Auth: `X-Auth-Secret` header (single shared secret). Read-only.

- `GET /healthz`
- `GET /users/{email}/status` — token health, refresh state
- `POST /users/{email}/connect/start|{sid}|{sid}/complete` — OAuth flow
- `POST /users/{email}/refresh`
- `GET /users/{email}/quote/{symbol}` → `{symbol, description, mark, bid, ask, last, net_change, net_change_pct, volume, low, high, 52wk_low, 52wk_high}`
- `GET /users/{email}/accounts` → `{account_id, type, cash_balance, market_value, positions[]}`
- `GET /users/{email}/positions` → `{symbol, description, asset_type, quantity, market_value, average_price}`

MCP mirror (`cassandra-schwab-mcp`): `session_status`, `quote`, `accounts`, `positions`, `balances` — MCP-key auth + owner-only ACL.

## Reachable with the existing token but NOT wrapped (schwab-py)

- **Orders**: list/get/place/replace/cancel/**preview** — the write path
- **Transactions**: 60-day activity history, filterable by type/symbol
- **Price history**: minute candles to ~48 days, daily/weekly/monthly beyond
- **Multi-symbol quotes** in one call (`/marketdata/v1/quotes?symbols=...` — current broker only does one at a time)
- **Movers** (per index), **market hours**, **instrument search/fundamentals**
- **Streaming**: `StreamClient` WebSocket — real-time quotes + account activity

## Market-research MCP — exists today

`https://market-research.cassandrasedge.com` (streamable-HTTP MCP), auth `Bearer mcp_<key>` (90-day, minted per-project in portal, validated by cassandra-auth). FMP premium (30 rps / 6k rpm / 1M day) + Polygon + FRED + ThetaData + TradingView proxy.

Most relevant for the app: `quote`, `intraday_prices` (adaptive 5m/15m/1h or uniform 1m–4h, ≤500 candles, VWAP/hi/lo summary), `price_history`, `market_news`, `earnings_calendar`, `dividends_calendar`, `sector_performance`, `index_performance`, `market_overview`, `market_hours`, plus options chains/Greeks (Polygon/ThetaData) if we ever show option positions.

## Auth story for the iOS app

- **Flock hub** (`:8787`): Tailscale-only today — phone joins the tailnet, no extra auth. (Optional CF Tunnel + Access for off-tailnet.)
- **Market-research MCP**: mint one `mcp_` key per device, store in Keychain. Portal already has key-minting (`POST /api/mcp-keys`); a `/api/mobile/login` mirror of the CLI login flow would automate it.
- **Broker**: the shared `X-Auth-Secret` should *not* ship in an app. Two options: route broker calls through the schwab-MCP (mcp-key auth, already owner-scoped), or put the broker behind the same mcp-key validation. The MCP route needs zero new code.

## The write path: approvals gate (to build)

Agents must never hold `place_order`. Flow:

```
flock agent → POST proposal → approvals-gate (store + expiry + push notif)
   → phone swipe right → gate: preview_order (validates buying power)
   → place_order → poll order status → "filled @ px" → feed + notif
```

Guardrails enforced in the gate, not the agent: owner-only, per-order notional cap, daily aggregate cap, market-hours check, idempotency keys on proposals, auto-deny on expiry. Broker additions: `POST /users/{email}/orders/preview`, `POST /users/{email}/orders`, `GET /users/{email}/orders`, `DELETE /users/{email}/orders/{id}`.
