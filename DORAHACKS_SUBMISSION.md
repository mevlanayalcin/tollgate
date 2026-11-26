# DoraHacks Submission — Tollgate (Gnome Tavern Hackathon)

- **Project Name:** Tollgate
- **Tagline (1 line):** On-chain HTTP 402 (`x402`) metering vaults, prepaid credit passes, subscriptions & verifiable SHA-256 receipts for Gno.land realms and AI agents.
- **GitHub Repository:** https://github.com/mevlanayalcin/tollgate
- **Live Web Explorer & 402 Playground:** https://mevlanayalcin.github.io/tollgate/
- **Live `onyx-1` Realm (`gnoweb`):** https://onyx.testnets.gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate
- **Live `onyx-1` Pure Package:** https://onyx.testnets.gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate
- **GPAS Verification:** https://gpas.onyx.testnets.gno.land/?path=gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate
- **Demo Video URL:** _(updated automatically on final release upload)_

## Problem & Innovation
`gnolang/x402` (Nov 2026) enabled HTTP 402 `ugnot` micropayments on Gno.land without realms, explicitly documenting two open exposures: signed payments never expire, and a payer can consume its sequence between `/verify` and `/settle`, plus every single HTTP call incurs L1 block latency and gas.

**Tollgate** bridges `gnolang/x402` with stateful Gno `v1.8.0-rc.1` realms:
1. **Prepaid Credit Passes (`OpenPass` / `TopUpPass` / `RequestEarlyClose` / `ClosePass`):** Buyers/AI agents lock `ugnot` on-chain with per-call caps, bounded expiry, and Gno `v1.8.0` `GetSessionInfo()` session-key tracking — enabling sub-50ms HTTP requests with 0 L1 gas per call, volume tier discounts (5%–20%), and zero sequence-race exposure.
2. **Challenge-Window Settlements & Disputes:** Providers batch-settle via `ClaimSettlement`; buyers get a configurable block challenge window to audit the canonical SHA-256 commitment and `DisputeSettlement` overcharges before `FinalizeSettlement` pays out.
3. **Fixed-Window Subscriptions & Native `x402` Anchoring:** Supports both upfront quota plans (`Subscribe`) and direct `gnolang/x402` facilitator payments anchored via `RecordX402Receipt` with on-chain anti-replay nonces.
4. **Full Developer & Agent Stack:** Pure `/p/` package + hardened `/r/` realm + Go SDK/middleware + TypeScript SDK + MCP JSON-RPC 2.0 stdio agent client + live Web Explorer (with byte-exact SHA-256 receipt verification across Gno, Go, TypeScript, and WebCrypto).
