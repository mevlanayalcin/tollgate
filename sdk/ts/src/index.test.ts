import test from "node:test";
import assert from "node:assert/strict";
import {
  computeCharge,
  formatInvoiceMemo,
  parseInvoiceMemo,
  hashResource,
  receiptCommitment,
  verifyReceiptCommitment,
  voucherMessage,
  generateVoucherKeypair,
  signVoucher,
  verifyVoucher,
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

test("Ed25519 voucher message, signing, verification, and tamper rejection", () => {
  const kp = generateVoucherKeypair();
  assert.equal(kp.publicKeyHex.length, 64);
  assert.equal(kp.seedHex.length, 64);

  const resHash = hashResource("GET /api/v1/oracle?pair=GNOT-USD");
  const params = {
    chainId: "onyx-1",
    serviceId: 1n,
    passId: 1n,
    subId: 0n,
    units: 10n,
    nonce: "ts-voucher-001",
    resourceHash: resHash,
  };
  assert.equal(
    voucherMessage(params),
    `tollgate-voucher-v1|6:onyx-1|1|1|0|10|14:ts-voucher-001|64:${resHash}|`
  );

  const sig = signVoucher(kp.seedHex, params);
  assert.equal(sig.length, 128);
  assert.equal(verifyVoucher(kp.publicKeyHex, sig, params), true);
  assert.equal(verifyVoucher(kp.publicKeyHex, sig, { ...params, units: 11n }), false);
  assert.equal(verifyVoucher(kp.publicKeyHex, sig, { ...params, nonce: "ts-voucher-002" }), false);
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
