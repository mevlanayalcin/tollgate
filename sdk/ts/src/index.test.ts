import test from "node:test";
import assert from "node:assert/strict";
import {
  computeCharge,
  formatInvoiceMemo,
  parseInvoiceMemo,
  hashResource,
  receiptCommitment,
  verifyReceiptCommitment,
  payWordStep,
  buildPayWordChain,
  payWordProofForAnchor,
  verifyPayWord,
  decodePaymentRequired,
} from "./index.ts";

test("computeCharge matches volume tier schedule", () => {
  const c = computeCharge(1000n, 10n);
  assert.equal(c.grossUgnot, 10000n);
  assert.equal(c.discountBps, 500n);
  assert.equal(c.netUgnot, 9500n);
});

test("invoice memo roundtrip", () => {
  const memo = formatInvoiceMemo(42n, "req_2026-11-26.001");
  assert.equal(memo, "tollgate:v1:42:req_2026-11-26.001");
  const parsed = parseInvoiceMemo(memo);
  assert.deepEqual(parsed, { serviceId: 42n, nonce: "req_2026-11-26.001" });
});

test("canonical receipt commitment matches Gno and Go SDKs byte-for-byte", () => {
  const resHash = hashResource("GET /v1/weather?city=zurich");
  const params = {
    chainId: "onyx-1",
    serviceId: 1n,
    passId: 7n,
    buyer: "g1buyer000000000000000000000000000000000",
    provider: "g1prov0000000000000000000000000000000000",
    units: 10n,
    amountUgnot: 9500n,
    nonce: "inv-0001",
    resourceHash: resHash,
  };
  const commitment = receiptCommitment(params);
  assert.equal(commitment.length, 64);
  assert.equal(verifyReceiptCommitment(commitment, params), true);
  assert.equal(commitment, "cfdf0dc69056f30d26543f18accf341c2f44d65fcd86a72a21f845c66010eafc");
});

test("PayWord SHA-256 hash-chain matches Gno & Go SDKs and rejects overcharge/replay", () => {
  const seed = hashResource("tollgate-payword-test-seed");
  const chain = buildPayWordChain("dev", 10, seed);
  assert.equal(chain.anchorHex.length, 64);
  assert.equal(payWordStep("dev", chain.hashes[9]), chain.anchorHex);

  const proof2 = payWordProofForAnchor(chain.hashes, chain.anchorHex, 2);
  assert.equal(verifyPayWord("dev", chain.anchorHex, proof2, 2n), true);
  // Overcharge (+1 unit) must fail
  assert.equal(verifyPayWord("dev", chain.anchorHex, proof2, 3n), false);
  // Wrong chainId must fail
  assert.equal(verifyPayWord("onyx-1", chain.anchorHex, proof2, 2n), false);
  // Next call advances cleanly from proof2
  const proofNext3 = payWordProofForAnchor(chain.hashes, proof2, 3);
  assert.equal(verifyPayWord("dev", proof2, proofNext3, 3n), true);
});

test("decodePaymentRequired decodes x402 v2 base64 header", () => {
  const b64 = Buffer.from(
    JSON.stringify({
      x402Version: 2,
      error: "Payment required",
      resource: { url: "https://api.example/v1", description: "Oracle", mimeType: "application/json" },
      accepts: [{ scheme: "exact", network: "gno:onyx-1", asset: "ugnot", amount: "1000", payTo: "g1abc", maxTimeoutSeconds: 300 }],
    })
  ).toString("base64");
  const pr = decodePaymentRequired(b64);
  assert.equal(pr.x402Version, 2);
  assert.equal(pr.accepts[0].network, "gno:onyx-1");
});
