# DoraHacks Submission — Tollgate (Gnome Tavern Hackathon)

- **Project Name:** Tollgate
- **Tagline (1 line):** On-chain HTTP 402 (`gnolang/x402`) metering vaults, Rivest-Shamir PayWord SHA-256 hash-chain prepaid credit passes, subscriptions & verifiable SHA-256 receipts for Gno.land realms and AI agents.
- **GitHub Repository:** https://github.com/mevlanayalcin/tollgate
- **Live Static Web Explorer (GitHub Pages):** https://mevlanayalcin.github.io/tollgate/ _(read-only `onyx-1` RPC explorer + WebCrypto receipt verifier; live HTTP 402 probes connect to a user-run local `tollgate-gateway`)_
- **Live `onyx-1` Realm (`gnoweb`):** https://onyx.testnets.gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2
- **Live `onyx-1` Pure Package:** https://onyx.testnets.gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate
- **GPAS Verification (`passed`):** https://gpas.onyx.testnets.gno.land/?path=gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2
- **HD Demo Video (1080p MP4, 2m15s):** https://github.com/mevlanayalcin/tollgate/releases/download/v1.0.0/tollgate-demo-1080p.mp4
- **Self-Published Static Analysis Certificate (Author's Companion Analyzer `r/.../ironclad` #6 — Grade A+ 100/100):** https://onyx.testnets.gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/ironclad:attestation/6 _(independently reproducible via `ironclad scan --rpc https://rpc.onyx.testnets.gno.land:443 --pkg gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate/v2`)_

## Live `onyx-1` On-Chain Proofs (`v1.8.0-rc.1` toolchain)
> _Transparency note:_ The 7 demo calls below were submitted directly via `gnokey` (`scripts/deploy_onyx.sh`) with the deployer address acting as buyer, provider, and `payTo`. Receipt `#3` (`x402-onyx-facilitator-demo-001`) is a synthetic `RecordX402Receipt` anchor demonstrating the schema (no external x402 facilitator payment occurred; stock x402 middleware interop is tested against a mock facilitator in `sdk/go/middleware_test.go`). Immutable `render.gno` headers on `tollgate/v2` still show legacy `"Ed25519"` column text from v1 while the active on-chain verifier executes pure `crypto/sha256` PayWord hash-chain proofs (`payword:<hash>`).

- **Deploy `/p/.../tollgate`:** Height `1418662` · TX `y7PvM1RcN0w+a93EUvpqzep2H5GrkczQRCpncVX0FQU=` (GPAS `passed`)
- **Deploy `/r/.../tollgate/v2`:** Height `1421205` · TX `VWXGKOXB6hlxXKhofmbpnlKyamaCRPpbtAIPNf/SIs0=` (GPAS `passed`)
- **`RegisterService("alpha-oracle")`:** Height `1422294` · TX `nP5+OsivBgOVr0wG1XSPtkAsYr7VXN4pqrp2Uxxl9TE=`
- **`OpenPass(#1, 50,000 ugnot, PayWord Chain #1)`:** Height `1422296` · TX `qU0c1+sl2trG7rYnwj9mFsUBR7PImsBoZJTWTf94xGM=`
- **`Subscribe(#1, 10,000 ugnot, PayWord Chain #2)`:** Height `1422298` · TX `Wel/RCvaneYkZkz9ySJlQ+PkKGDH3rhCT99qBvQdP6k=`
- **`ClaimSettlement(#1, 10u -> 9,500 ugnot, PayWord verified)`:** Height `1422300` · TX `swm9I/8Aih5TQdwM+5nW8J9sngL79KxDLNoKiarfcY0=` · Receipt `#1` (`1dc2137f9c441e991f8dd10f71c74ffcf34696a3e65c46ef77e86aea9ab9a100`, `buyerSigned=true`)
- **`ConsumeSubscription(#1, 2u, PayWord verified)`:** Height `1422302` · TX `9uPRWiOuAhFV3Kmd4Rkq8br/Nz/m9xP5UuhwFuv2vuk=` · Receipt `#2` (`fde68708c7308d3cf8b02cc41067cb0a301cbb65c0cbc5110c0c249a861c942c`, `buyerSigned=true`)
- **`RecordX402Receipt(#3, 1,000 ugnot, synthetic demo anchor)`:** Height `1422304` · TX `71xFO4lD5wcYGRs7MfgkFouTxq2NH0721mZ03yafy8c=` · Receipt `#3` (`50c03cd6bcc8672c3c86791decece1f433b5dd69ae6a56934b455580e22c923a`)
- **`FinalizeSettlement(#1, 9,500 ugnot escrow payout)`:** Height `1422323` · TX `XP9CjvtVuDUwPkbmFAJ+msuKYExILFw2QlZsSC6R9zw=`

## Problem & Innovation
`gnolang/x402` (Nov 2026) enabled HTTP 402 `ugnot` micropayments on Gno.land without realms, noting three trade-offs: no realm state, sequence-race exposure between `/verify` and `/settle`, and per-request L1 block latency and gas.

**Tollgate** bridges `gnolang/x402` with stateful Gno realms:
1. **PayWord SHA-256 Hash-Chain Credit Passes (`OpenPass` / `TopUpPass` / `RequestEarlyClose` / `ClosePass`):** Buyers lock `ugnot` in realm escrow with per-call caps, bounded expiry, and pure `crypto/sha256` Rivest-Shamir PayWord roots (GPAS-native, cryptographic overcharge resistance, volume tiers 5%–20%).
2. **Challenge-Window Settlements & Non-Freezing Disputes:** Providers settle via `ClaimSettlement`; buyers may call `DisputeSettlement`, and unresolved disputes unlock via `ReclaimDisputedSettlement` after timeout so passes can never freeze permanently.
3. **Upfront Subscriptions & Stock `x402` v2 Interop:** Supports upfront quota plans (`Subscribe` / `ConsumeSubscription`) and stock `gnolang/x402` direct payment anchoring (`ExtractX402AcceptedMemo` + `RecordX402Receipt` tracked separately from on-chain revenue).
4. **Resilient Developer & AI Agent Stack:** Pure `/p/` + hardened `/r/tollgate/v2`, per-pass serialized Go gateway with persistent state, crash-recovery reconciliation & `GnokeySubmitter` deferred settler, stateful `tollgate-agent` + MCP JSON-RPC 2.0 stdio server, TypeScript SDK with shared golden vectors, and live Web Explorer with WebCrypto SHA-256 receipt verification.
