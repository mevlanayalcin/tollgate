import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
  sign,
  timingSafeEqual,
  verify,
} from "node:crypto";

export const PROTOCOL_VERSION = "tollgate-v1";
export const RECEIPT_DOMAIN = "tollgate-receipt-v1";
export const VOUCHER_DOMAIN = "tollgate-voucher-v1";
export const MEMO_PREFIX = "tollgate:v1:";
export const MAX_MEMO_BYTES = 256;

const ED25519_PKCS8_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const ED25519_SPKI_PREFIX = Buffer.from("302a300506032b6570032100", "hex");

export interface ChargeBreakdown {
  units: bigint;
  unitPrice: bigint;
  grossUgnot: bigint;
  discountBps: bigint;
  discount: bigint;
  netUgnot: bigint;
}

export interface PaymentRequirements {
  scheme: string;
  network: string;
  asset: string;
  amount: string;
  payTo: string;
  maxTimeoutSeconds: number;
  extra?: Record<string, unknown>;
}

export interface PaymentRequired {
  x402Version: number;
  error: string;
  resource: {
    url: string;
    description: string;
    mimeType: string;
  };
  accepts: PaymentRequirements[];
}

export function tierDiscountBps(units: bigint): bigint {
  if (units >= 1000n) return 2000n;
  if (units >= 250n) return 1500n;
  if (units >= 50n) return 1000n;
  if (units >= 10n) return 500n;
  return 0n;
}

export function computeCharge(unitPrice: bigint, units: bigint): ChargeBreakdown {
  if (unitPrice <= 0n) throw new Error("tollgate: unit price must be positive");
  if (units <= 0n) throw new Error("tollgate: units must be positive");
  const gross = unitPrice * units;
  const discountBps = tierDiscountBps(units);
  let discount = discountBps > 0n ? (gross * discountBps) / 10000n : 0n;
  let net = gross - discount;
  if (net < 1n) {
    net = 1n;
    discount = gross - net;
  }
  return {
    units,
    unitPrice,
    grossUgnot: gross,
    discountBps,
    discount,
    netUgnot: net,
  };
}

export function formatInvoiceMemo(serviceId: bigint, nonce: string): string {
  if (serviceId <= 0n || !/^[a-zA-Z0-9._-]{1,96}$/.test(nonce)) {
    throw new Error("tollgate: invalid serviceId or nonce");
  }
  const memo = `${MEMO_PREFIX}${serviceId.toString()}:${nonce}`;
  if (Buffer.byteLength(memo, "utf8") > MAX_MEMO_BYTES) {
    throw new Error("tollgate: memo exceeds 256 bytes");
  }
  return memo;
}

export function parseInvoiceMemo(memo: string): { serviceId: bigint; nonce: string } | null {
  if (Buffer.byteLength(memo, "utf8") > MAX_MEMO_BYTES || !memo.startsWith(MEMO_PREFIX)) {
    return null;
  }
  const rest = memo.slice(MEMO_PREFIX.length);
  const idx = rest.indexOf(":");
  if (idx <= 0 || idx >= rest.length - 1) return null;
  const rawId = rest.slice(0, idx);
  const nonce = rest.slice(idx + 1);
  if (!/^[0-9]+$/.test(rawId) || !/^[a-zA-Z0-9._-]{1,96}$/.test(nonce)) return null;
  const serviceId = BigInt(rawId);
  if (serviceId <= 0n) return null;
  return { serviceId, nonce };
}

function lp(s: string): string {
  return `${Buffer.byteLength(s, "utf8")}:${s}|`;
}

export function hashResource(descriptor: string): string {
  return createHash("sha256").update(descriptor, "utf8").digest("hex");
}

export function receiptCommitment(params: {
  chainId: string;
  serviceId: bigint;
  passId: bigint;
  buyer: string;
  provider: string;
  units: bigint;
  amountUgnot: bigint;
  nonce: string;
  resourceHash: string;
}): string {
  const preimage =
    `${RECEIPT_DOMAIN}|` +
    lp(params.chainId) +
    `${params.serviceId.toString()}|` +
    `${params.passId.toString()}|` +
    lp(params.buyer) +
    lp(params.provider) +
    `${params.units.toString()}|` +
    `${params.amountUgnot.toString()}|` +
    lp(params.nonce) +
    lp(params.resourceHash);
  return createHash("sha256").update(preimage, "utf8").digest("hex");
}

export function verifyReceiptCommitment(
  expectedHex: string,
  params: Parameters<typeof receiptCommitment>[0]
): boolean {
  if (!/^[0-9a-f]{64}$/.test(expectedHex)) return false;
  const got = receiptCommitment(params);
  return timingSafeEqual(Buffer.from(got, "utf8"), Buffer.from(expectedHex, "utf8"));
}

export function voucherMessage(params: {
  chainId: string;
  serviceId: bigint;
  passId: bigint;
  subId: bigint;
  units: bigint;
  nonce: string;
  resourceHash: string;
}): string {
  return (
    `${VOUCHER_DOMAIN}|` +
    lp(params.chainId) +
    `${params.serviceId.toString()}|` +
    `${params.passId.toString()}|` +
    `${params.subId.toString()}|` +
    `${params.units.toString()}|` +
    lp(params.nonce) +
    lp(params.resourceHash)
  );
}

export function generateVoucherKeypair(): { publicKeyHex: string; seedHex: string } {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const spki = publicKey.export({ format: "der", type: "spki" });
  const pkcs8 = privateKey.export({ format: "der", type: "pkcs8" });
  return {
    publicKeyHex: spki.subarray(spki.length - 32).toString("hex"),
    seedHex: pkcs8.subarray(pkcs8.length - 32).toString("hex"),
  };
}

export function signVoucher(
  seedOrPrivKeyHex: string,
  params: Parameters<typeof voucherMessage>[0]
): string {
  const raw = Buffer.from(seedOrPrivKeyHex.trim(), "hex");
  const seed = raw.length === 64 ? raw.subarray(0, 32) : raw;
  if (seed.length !== 32) {
    throw new Error("tollgate: expected 32-byte Ed25519 seed or 64-byte private key hex");
  }
  const keyObj = createPrivateKey({
    key: Buffer.concat([ED25519_PKCS8_PREFIX, seed]),
    format: "der",
    type: "pkcs8",
  });
  const msg = Buffer.from(voucherMessage(params), "utf8");
  return sign(null, msg, keyObj).toString("hex");
}

export function verifyVoucher(
  publicKeyHex: string,
  sigHex: string,
  params: Parameters<typeof voucherMessage>[0]
): boolean {
  if (!/^[0-9a-f]{64}$/.test(publicKeyHex) || !/^[0-9a-f]{128}$/.test(sigHex)) {
    return false;
  }
  const pub = Buffer.from(publicKeyHex, "hex");
  const sig = Buffer.from(sigHex, "hex");
  const keyObj = createPublicKey({
    key: Buffer.concat([ED25519_SPKI_PREFIX, pub]),
    format: "der",
    type: "spki",
  });
  const msg = Buffer.from(voucherMessage(params), "utf8");
  return verify(null, msg, keyObj, sig);
}

export function decodePaymentRequired(headerValue: string): PaymentRequired {
  const jsonText = Buffer.from(headerValue.trim(), "base64").toString("utf8");
  return JSON.parse(jsonText) as PaymentRequired;
}

export class TollgateClient {
  readonly rpcUrl: string;
  readonly realmPath: string;

  constructor(rpcUrl: string, realmPath: string) {
    this.rpcUrl = rpcUrl.replace(/\/+$/, "");
    this.realmPath = realmPath.replace(/^\/+|\/+$/g, "");
  }

  async queryRender(subPath: string): Promise<string> {
    const dataHex = Buffer.from(`${this.realmPath}:${subPath}`, "utf8").toString("hex");
    const url = `${this.rpcUrl}/abci_query?path=${encodeURIComponent(vm/qrender)}&data=0x${dataHex}`;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`RPC HTTP ${res.status}`);
    const body = (await res.json()) as {
      result?: { response?: { ResponseBase?: { Data?: string; Error?: unknown } } };
    };
    const base = body.result?.response?.ResponseBase;
    if (base?.Error) throw new Error(`ABCI error: ${JSON.stringify(base.Error)}`);
    return base?.Data ? Buffer.from(base.Data, "base64").toString("utf8") : "";
  }

  async getSummary<T = Record<string, unknown>>(): Promise<T> {
    return JSON.parse(await this.queryRender("json/summary")) as T;
  }

  async getServices<T = Array<Record<string, unknown>>>(): Promise<T> {
    return JSON.parse(await this.queryRender("json/services")) as T;
  }

  async fetchWithPass(
    url: string,
    opts: {
      passId?: bigint;
      subId?: bigint;
      units?: bigint;
      nonce?: string;
      voucherSigHex?: string;
    }
  ): Promise<{
    response: Response;
    rail: string | null;
    commitment: string | null;
    nonce: string | null;
    resourceHash: string | null;
    netUgnot: string | null;
  }> {
    const headers = new Headers();
    if (opts.subId !== undefined) headers.set("X-Tollgate-Sub", opts.subId.toString());
    else if (opts.passId !== undefined) headers.set("X-Tollgate-Pass", opts.passId.toString());
    if (opts.units !== undefined) headers.set("X-Tollgate-Units", opts.units.toString());
    if (opts.nonce) headers.set("X-Tollgate-Nonce", opts.nonce);
    if (opts.voucherSigHex) headers.set("X-Tollgate-Voucher-Sig", opts.voucherSigHex);

    const response = await fetch(url, { headers });
    return {
      response,
      rail: response.headers.get("X-Tollgate-Rail"),
      commitment: response.headers.get("X-Tollgate-Receipt-Commitment"),
      nonce: response.headers.get("X-Tollgate-Nonce"),
      resourceHash: response.headers.get("X-Tollgate-Resource-Hash"),
      netUgnot: response.headers.get("X-Tollgate-Net-Ugnot"),
    };
  }
}
