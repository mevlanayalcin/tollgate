# Tollgate — On-Chain `x402` Metering, PayWord SHA-256 Credit Vault & Receipt Protocol for Gno.land

> Built for the **Gno.land Gnome Tavern Hackathon (Nov–Dec 2026)** on Gno `v1.8.0-rc.1` (`onyx-1`).

**Tollgate** extends [`gnolang/x402`](https://github.com/gnolang/x402) from stateless per-request HTTP 402 bank transfers into a **stateful, realm-backed metering, subscription, and cryptographic receipt protocol** on Gno.land.

---

## Why Tollgate?

The official `gnolang/x402` README notes three trade-offs of stateless per-request HTTP 402 bank transfers:

1. **No realm / contract state:** *"no realm and no contract call are involved."*
2. **Sequence-race exposure:** *"A payer can consume the signed-over sequence between `/verify` and `/settle`. Settlement then fails after the seller's handler has run, so the seller does the work unpaid."*
3. **Per-call L1 latency & gas:** Every individual API call waits for an on-chain block confirmation and pays a separate L1 gas fee, and unused signed transactions never expire on their own.

**Tollgate complements direct `x402` with three interoperable rails:**

| Rail | On-Chain Settlement Cadence | Buyer Auth | Settlement Model | Best For |
|---|---|---|---|---|
| **1. Subscription Quota** (`X-Tollgate-Sub`) | Upfront payment + batched `ConsumeSubscription` receipts | On-chain PayWord SHA-256 hash-chain (`crypto/sha256`) | Paid upfront directly to `payTo` (immediate settlement) | Predictable high-volume API & agent workloads |
| **2. Prepaid Credit Pass** (`X-Tollgate-Pass`) | Single vault deposit + batched `ClaimSettlement` with challenge window | On-chain PayWord SHA-256 hash-chain (`crypto/sha256`) | Funds locked in realm escrow | Pay-as-you-go metering with 5%–20% volume tiers & buyer dispute window |
| **3. Direct `gnolang/x402` v2** (`PAYMENT-SIGNATURE`) | Per-request via facilitator `/verify` & `/settle` | Signed Gno bank-send tx | Standard `x402` facilitator semantics | Zero-onboarding, one-shot stock `x402` callers |

---

## Trust Model & Settlement Guarantees

- **Rivest-Shamir PayWord SHA-256 Hash-Chain Vouchers (GPAS-Native, `O(1)` Batch Proofs):** When opening a pass (`OpenPass`) or subscription (`Subscribe`), the buyer derives a domain-separated SHA-256 hash chain `w_i = SHA256("tollgate-payword-v1|" + chainID + "|" + w_{i-1})` (`i = 1..N`) and anchors `w_N` on-chain (`voucherPubKey`). To authorize `u` units (`1 <= u <= 1024`), the buyer reveals `w_{pos - u}` in `X-Tollgate-Voucher-Sig`. Both the off-chain gateway and the on-chain realm (`ClaimSettlement`, `ConsumeSubscription`) verify `SHA256^u(preimage) == currentAnchor` and advance the anchor to `preimage`.
  - **Cryptographic overcharge resistance:** A provider holding `w_{pos - u}` cannot claim `u + 1` units without inverting SHA-256.
  - **Zero native Ed25519 dependency & `O(1)` batch settlement:** Uses only pure `crypto/sha256` (passing `onyx-1` GPAS automatic verification immediately) and allows multiple sequential calls to settle atomically.
  - Whenever a pass or subscription binds a PayWord root (enforced by default via gateway `--require-auth=true`; empty anchor remains optional on-chain for legacy flows), neither a third party nor a provider can claim funds without a valid buyer preimage. Use a distinct PayWord seed per pass/subscription.
- **Challenge Window & Non-Freezing Disputes:** Every `ClaimSettlement` locks funds into `pendingUgnot` for `svc.challengeBlocks`. During the window, the buyer may call `DisputeSettlement`. The provider may concede via `RefundDisputedSettlement`, or if unresolved after an additional `svc.challengeBlocks`, the buyer may call `ReclaimDisputedSettlement` so disputed claims never permanently freeze a pass. _Optimistic-dispute trade-off:_ a buyer can unilaterally dispute and reclaim without cryptographic proof of non-delivery; providers mitigate this via short challenge windows, reputation, or the upfront-settled subscription rail. Refunded receipts return `false` from `VerifyReceiptByCommitment`.
- **Bounded Claims & Strict Denom Safety:** `ClaimSettlement` rejects claims at/after `p.expiryHeight` (protecting `ClosePass`), rejects foreign non-`ugnot` denoms in `OriginSend()`, caps single-tx `units` and `amountUgnot` with overflow-checked arithmetic, and defaults `maxPerCallUgnot <= 0` to `svc.unitPriceUgnot`.
- **Honest Revenue Accounting:** On-chain realm revenue (settled pass claims + upfront subscription payments) is tracked separately from external provider-attested `RecordX402Receipt` anchors (`x402AttestedRevenue`).
- **Resilient Gateway & Admin Controls:** `tollgate-gateway` persists used nonces, local PayWord anchors, and unsettled receipts atomically to `--state-file`, serializes settlement flushes with dead-letter skipping (`--auto-settle=15s` when a submitter is configured), and restricts `/api/settle-now` and `/api/explorer/pending-receipts` to loopback or `--admin-token` (with voucher proofs redacted in API responses).

---

## Quick Start & Verification

```bash
# 1. Run Gno unit tests & linter (v1.8.0-rc.1)
make gno-test && make gno-lint

# 2. Build binaries and run Go race tests + TypeScript SDK tests
make build && make go-test && make ts-test

# 3. Run full end-to-end test on live local gnodev v1.8.0-rc.1
#    (deploys /p/tollgate & /r/tollgate/v2, generates PayWord SHA-256 chain,
#     opens pass & sub, verifies 401 on unsigned, 200 on PayWord pass & sub,
#     409/401 on replay, checks MCP stdio, flushes real on-chain settlement via
#     GnokeySubmitter, verifies receipts on-chain & checks live gnoweb rendering)
make e2e
```

---

## Live `onyx-1` Deployment & Links

- **Pure Package:** `gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate` (GPAS `passed`, live)
- **Stateful Realm:** `gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2` (GPAS `passed`, TX `VWXGKOXB6hlxXKhofmbpnlKyamaCRPpbtAIPNf/SIs0=`, Height `1421205`)
- **gnoweb Explorer:** `https://onyx.testnets.gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2`
- **GPAS Verification:** `https://gpas.onyx.testnets.gno.land/?path=gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2`
- **Interactive Web Explorer:** `https://mevlanayalcin.github.io/tollgate/`
- **x402 Facilitator:** `https://facilitator.onyx.testnets.gno.land` (`gno:onyx-1`)
