# Tollgate — On-Chain `x402` Metering, Prepaid Credit Vault & Verifiable Receipt Protocol for Gno.land

> Built for the **Gno.land Gnome Tavern Hackathon (Nov–Dec 2026)** on Gno `v1.8.0-rc.1` (`onyx-1`).

**Tollgate** extends [`gnolang/x402`](https://github.com/gnolang/x402) from stateless per-request HTTP 402 bank transfers into a **stateful, realm-backed metering, subscription, and cryptographic receipt protocol** on Gno.land.

---

## Why Tollgate?

The official `gnolang/x402` README highlights three structural gaps of pure stateless bank-send HTTP 402 payments:

1. **No realm / contract state:** *"no realm and no contract call are involved."*
2. **Sequence-race exposure:** *"A payer can consume the signed-over sequence between `/verify` and `/settle`. Settlement then fails after the seller's handler has run, so the seller does the work unpaid."*
3. **No transaction expiry & per-call L1 latency:** Every single API request waits for an on-chain block confirmation and pays a full L1 gas fee, and unused signed transactions never expire on their own.

**Tollgate solves all three with a unified Dual-Rail Architecture:**

| Rail | Latency | L1 Gas / Call | Double-Spend / Sequence Risk | Best For |
|---|---|---|---|---|
| **1. Subscription Quota** (`X-Tollgate-Sub`) | `< 50ms` | `0` per call (batched receipts) | Eliminated (upfront `ugnot` paid) | Recurring human & agent API consumers |
| **2. Prepaid Credit Pass** (`X-Tollgate-Pass`) | `< 50ms` | `0` per call (batched settlement + up to 20% volume discount) | Eliminated (locked on-chain `ugnot` vault + bounded expiry + challenge window) | High-frequency AI agents, tool calls, metered inference |
| **3. Native `gnolang/x402` Direct** (`PAYMENT-SIGNATURE`) | 1 block | 1 tx / call | Handled via `onyx-1` facilitator + on-chain `RecordX402Receipt` | Cold one-off `@gnolang/x402` clients |

---

## Architecture

```mermaid
flowchart LR
    Agent["AI Agent / Browser / MCP Client"] -->|1. GET /api/v1/oracle| GW["Tollgate HTTP 402 Gateway (Go SDK)"]
    GW -->|2. HTTP 402 + PAYMENT-REQUIRED + X-Tollgate-*| Agent
    Agent -->|3a. X-Tollgate-Pass / X-Tollgate-Sub| GW
    Agent -->|3b. PAYMENT-SIGNATURE (x402 v2)| GW
    GW -->|vm/qrender + vm/qeval| Realm["gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate"]
    GW -->|/verify + /settle| Fac["facilitator.onyx.testnets.gno.land"]
    Realm -->|Imports pure math & SHA-256 commitments| Pkg["gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate"]
    GW -->|4. HTTP 200 + X-Tollgate-Receipt-Commitment| Agent
    Realm -->|ClaimSettlement -> Challenge Window -> Finalize| PayTo["Provider PayTo Address"]
```

---

## Components

| Component | Path | Highlights |
|---|---|---|
| **Pure Gno Package** | [`gno/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`](gno/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/tollgate.gno) | Overflow-checked tiered volume discounts (5%–20%), x402 `extra.memo` codec (`tollgate:v1:<svc>:<nonce>`), length-prefixed domain-separated SHA-256 `ReceiptCommitment`, constant-time verification |
| **Stateful Gno Realm** | [`gno/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`](gno/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/tollgate.gno) | Service registry, prepaid credit passes (`OpenPass`, `TopUpPass`, `RequestEarlyClose`, `ClosePass`), subscriptions (`Subscribe`, `ConsumeSubscription`), challenge-window settlements (`ClaimSettlement`, `DisputeSettlement`, `FinalizeSettlement`, `RefundDisputedSettlement`), `x402` anchoring (`RecordX402Receipt`), Gno `v1.8.0` `GetSessionInfo()` session-key telemetry, sanitized `gnoweb` explorer + JSON views |
| **Go SDK & Middleware** | [`sdk/go/`](sdk/go/) | Byte-exact SHA-256 commitment library, Gno ABCI `vm/qrender`/`vm/qeval` client, `x402 v2` facilitator bridge, dual-rail `net/http` middleware |
| **Live Gateway & Agent/MCP CLI** | [`cmd/tollgate-gateway/`](cmd/tollgate-gateway/main.go), [`cmd/tollgate-agent/`](cmd/tollgate-agent/main.go) | Demo metered API (`/api/v1/oracle`, `/api/v1/weather`, `/api/v1/inference`), autonomous 402-negotiating CLI, and **MCP JSON-RPC 2.0 stdio server** (`tollgate_paid_fetch`, `tollgate_chain_summary`) |
| **TypeScript SDK** | [`sdk/ts/`](sdk/ts/src/index.ts) | Zero-dependency ESM TypeScript client with identical SHA-256 commitment verification |
| **Web Explorer & 402 Playground** | [`web/`](web/index.html) | Connects directly to `onyx-1` RPC + gateway; probes HTTP 402 headers, executes metered calls, and verifies receipts via WebCrypto |

---

## Security Engineering (Gno `v1.8.0-rc.1` Hardened)

Tollgate follows every rule of Gno's November 2026 `gno-security.md` and `gno-ai-contract-review.md` (and scores **100/100 Grade A+** under static security analysis):

- **Strict Capability Guards:** Every mutating function calls `cur.IsCurrent()`.
- **Direct EOA Payment Guard:** `OpenPass`, `TopUpPass`, and `Subscribe` require `cur.Previous().IsUserCall()` before reading `unsafe.OriginSend()`, eliminating ephemeral `MsgRun` phantom-payment attacks.
- **No Persisted Bankers / Realms:** `banker.NewBanker(banker.BankerTypeRealmSend, cur)` is instantiated fresh per transfer and never stored in package variables.
- **O(1) Single-Denom Balance Reads:** Uses `banker.NewReadonlyBanker().GetCoin(addr, "ugnot")` — never `GetCoins()` (immune to unsolicited-denom gas DoS).
- **Zero Exported State / Pointers:** All `avl.Tree` stores are unexported (`GLOBAL001`-clean); public getters return value copies only.
- **Bounded Iteration & Sanitized Markdown:** `Render(path)` uses `ReverseIterateByOffset` (`AVL001`-clean) and `gno.land/p/nt/markdown/sanitize/v0` on every user-supplied slot.
- **Cross-Language Canonical Receipt Vector:** Identical SHA-256 output across Gno `/p/`, Go SDK, TypeScript SDK, and WebCrypto (`cfdf0dc69056f30d26543f18accf341c2f44d65fcd86a72a21f845c66010eafc`).

---

## Quick Start & Reproducible Verification

```bash
# 1. Run Gno unit tests & linter (exact onyx-1 v1.8.0-rc.1 toolchain)
make gno-test
make gno-lint

# 2. Run Go SDK + middleware tests (with race detector) & TypeScript SDK tests
make go-test
make ts-test

# 3. Run full end-to-end test on live local gnodev v1.8.0-rc.1
#    (deploys /p/ -> /r/, registers service, opens pass, subscribes, runs gateway,
#     runs agent 402->200 + MCP server, settles on-chain & verifies SHA-256 proof)
make e2e
```

---

## Live `onyx-1` Deployment

- **Pure Package:** `gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`
- **Stateful Realm:** `gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`
- **gnoweb Explorer:** `https://onyx.testnets.gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`
- **GPAS Verification:** `https://gpas.onyx.testnets.gno.land/?path=gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate`
- **x402 Facilitator:** `https://facilitator.onyx.testnets.gno.land` (`gno:onyx-1`)
