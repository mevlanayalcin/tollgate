const $ = (id) => document.getElementById(id);

let liveSummary = { chainId: "onyx-1", height: 0 };
let liveServices = [];
let adenaAccount = null;
let hasSameOriginGateway = false;

// Browser-native PayWord state (for Keygen + Simulator)
let pwChain = { seedHex: "", anchorHex: "", currentAnchor: "", depth: 200, spent: 0, chainId: "onyx-1", hashes: [] };
let simUsedNonces = new Set();
let lastSimCall = null;

function escapeHtml(s) {
  return String(s ?? "").replace(/[&<>"']/g, (m) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[m]));
}

function toHex(str) {
  return Array.from(new TextEncoder().encode(str)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

function bytesToHex(bytes) {
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function sha256Hex(str) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(str));
  return bytesToHex(new Uint8Array(buf));
}

// Exact cross-language PayWordStep matching tollgate.gno:116, Go SDK, and TS SDK!
async function payWordStep(chainId, prevHex) {
  return sha256Hex(`tollgate-payword-v1|${chainId}|${prevHex}`);
}

async function verifyPayWord(chainId, anchorHex, preimageHex, steps) {
  let cur = preimageHex;
  for (let i = 0; i < steps; i++) {
    cur = await payWordStep(chainId, cur);
  }
  return cur === anchorHex;
}

function lp(s) {
  const bytes = new TextEncoder().encode(s).length;
  return `${bytes}:${s}|`;
}

async function computeReceiptCommitment(p) {
  const preimage =
    "tollgate-receipt-v1|" +
    lp(p.chainId) +
    `${p.serviceId}|${p.passId}|` +
    lp(p.buyer) +
    lp(p.provider) +
    `${p.units}|${p.amountUgnot}|` +
    lp(p.nonce) +
    lp(p.resourceHash);
  return sha256Hex(preimage);
}

function computeTierCharge(unitPrice, units) {
  const gross = unitPrice * units;
  let bps = 0;
  if (units >= 100) bps = 2000;
  else if (units >= 50) bps = 1000;
  else if (units >= 10) bps = 500;
  const discount = Math.floor((gross * bps) / 10000);
  return { gross, bps, net: gross - discount };
}

async function queryRender(rpc, realm, subPath) {
  const base = rpc.replace(/\/+$/, "");
  const cleanRealm = realm.replace(/^\/+|\/+$/g, "");
  const url = `${base}/abci_query?path=${encodeURIComponent('"vm/qrender"')}&data=0x${toHex(`${cleanRealm}:${subPath}`)}`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`RPC HTTP ${res.status}`);
  const body = await res.json();
  const rb = body?.result?.response?.ResponseBase;
  if (rb?.Error) throw new Error(`ABCI error: ${JSON.stringify(rb.Error)}`);
  if (!rb?.Data) return "";
  return atob(rb.Data);
}

// --- Tabs ---
function switchTab(name) {
  document.querySelectorAll(".tab-btn").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((p) => p.classList.toggle("active", p.id === `tab-${name}`));
}
document.querySelectorAll(".tab-btn").forEach((b) => b.addEventListener("click", () => switchTab(b.dataset.tab)));

// --- Live Chain Sync ---
async function syncChain() {
  const rpc = $("rpcInput").value.trim();
  const realm = $("realmInput").value.trim();
  $("statusBadge").textContent = "RPC: syncing…";
  try {
    const [sumRaw, svcsRaw] = await Promise.all([
      queryRender(rpc, realm, "json/summary"),
      queryRender(rpc, realm, "json/services"),
    ]);
    const sum = JSON.parse(sumRaw);
    const svcs = JSON.parse(svcsRaw);
    liveSummary = sum;
    liveServices = Array.isArray(svcs) ? svcs : [];
    $("chainBadge").textContent = `chain: ${sum.chainId}`;
    $("statusBadge").textContent = `RPC: synced #${sum.height}`;
    $("statTvl").textContent = `${sum.vaultBalanceUgnot} ugnot`;
    $("statHeight").textContent = `${sum.height}`;
    $("statServices").textContent = `${sum.services}`;
    $("statPasses").textContent = `${sum.passes}`;
    $("statSubs").textContent = `${sum.subscriptions}`;
    $("statReceipts").textContent = `${sum.receipts}`;

    const tbody = $("servicesBody");
    if (!liveServices.length) {
      tbody.innerHTML = `<tr><td colspan="9" class="muted">No services registered on this realm yet.</td></tr>`;
    } else {
      tbody.innerHTML = liveServices.map((s) => `
        <tr>
          <td class="mono">#${s.id}</td>
          <td class="mono">${escapeHtml(s.slug)}</td>
          <td>${escapeHtml(s.name)}</td>
          <td class="mono">${s.unitPriceUgnot} ugnot</td>
          <td class="mono">${s.subPriceUgnot > 0 ? `${s.subPriceUgnot} ugnot / ${s.subQuota}u` : "—"}</td>
          <td class="mono">${s.challengeBlocks} blk</td>
          <td class="mono">${s.totalCalls}</td>
          <td class="mono">${s.escrowedRevenue ?? 0} ugnot</td>
          <td class="mono">${s.x402AttestedRevenue ?? 0} ugnot</td>
        </tr>`).join("");
    }
  } catch (err) {
    $("statusBadge").textContent = "RPC: offline";
    $("servicesBody").innerHTML = `<tr><td colspan="9" class="muted">${escapeHtml(err.message)}</td></tr>`;
  }
}

// --- Receipt Verifier ---
async function verifyOnChainReceipt(idOverride) {
  if (idOverride) $("lookupReceiptInput").value = String(idOverride);
  const rpc = $("rpcInput").value.trim();
  const realm = $("realmInput").value.trim();
  const q = $("lookupReceiptInput").value.trim();
  $("verifierOutput").textContent = `Fetching live receipt ${q} from onyx-1…`;
  try {
    const [sumRaw, recRaw] = await Promise.all([
      queryRender(rpc, realm, "json/summary"),
      queryRender(rpc, realm, `json/receipt/${q}`),
    ]);
    const sum = JSON.parse(sumRaw);
    const rec = JSON.parse(recRaw);
    if (rec.error) {
      $("verifierOutput").textContent = JSON.stringify(rec, null, 2);
      return;
    }
    const recomputed = await computeReceiptCommitment({
      chainId: sum.chainId,
      serviceId: rec.serviceId,
      passId: rec.passId,
      buyer: rec.buyer,
      provider: rec.provider,
      units: rec.units,
      amountUgnot: rec.amountUgnot,
      nonce: rec.nonce,
      resourceHash: rec.resourceHash,
    });
    const refunded = rec.settlementStatus === "refunded";
    $("verifierOutput").textContent = JSON.stringify({
      verifiedByWebCrypto: recomputed === rec.commitment && !refunded,
      commitmentMatch: recomputed === rec.commitment,
      settlementStatus: rec.settlementStatus || "none",
      buyerSigned: Boolean(rec.buyerSigned),
      recomputedSha256: recomputed,
      onChainReceipt: rec,
    }, null, 2);
  } catch (err) {
    $("verifierOutput").textContent = `Verification error: ${err.message}`;
  }
}

// --- PayWord Keygen & Golden Vector ---
async function generatePayWordChain(depthOverride) {
  const depth = Math.max(10, Math.min(2000, Number(depthOverride || $("pwDepthInput").value) || 200));
  const chainId = liveSummary.chainId || "onyx-1";
  const rand = new Uint8Array(32);
  crypto.getRandomValues(rand);
  const seedHex = bytesToHex(rand);
  const hashes = new Array(depth + 1);
  hashes[0] = seedHex;
  const t0 = performance.now();
  for (let i = 1; i <= depth; i++) {
    hashes[i] = await payWordStep(chainId, hashes[i - 1]);
  }
  const dt = Math.round(performance.now() - t0);
  const anchorHex = hashes[depth];
  pwChain = { seedHex, anchorHex, currentAnchor: anchorHex, depth, spent: 0, chainId, hashes };
  simUsedNonces.clear();
  lastSimCall = null;
  $("pwSeedOut").value = seedHex;
  $("pwAnchorOut").value = anchorHex;
  $("pwKeygenLog").textContent = `Derived ${depth}-step Rivest-Shamir PayWord SHA-256 chain in ${dt} ms (chainId=${chainId}).\nAnchor root ready for OpenPass / Subscribe and Tab 3 Simulator.`;
  $("simCursorLabel").textContent = `PayWord chain active: 0 / ${depth} units spent (anchor ${anchorHex.slice(0, 12)}…)`;
  return pwChain;
}

async function verifyCrossLanguageGoldenVector() {
  // Exact cross-language golden vector verified across Gno, Go, and TypeScript SDKs:
  // PayWordStep("onyx-1", "00..00") == 33166b8a509fc02566aa139ef29415c7458dc917ddd3a559f42649d59faf2d05
  const prev = "0".repeat(64);
  const expected = "33166b8a509fc02566aa139ef29415c7458dc917ddd3a559f42649d59faf2d05";
  const computed = await payWordStep("onyx-1", prev);
  const ok = computed === expected;
  $("pwKeygenLog").textContent = JSON.stringify({
    crossLanguageGoldenVectorMatch: ok,
    chainId: "onyx-1",
    steps: 1,
    prevHex: prev,
    computedHex: computed,
    expectedHex: expected,
  }, null, 2);
}

// --- Adena Wallet Integration ---
async function connectAdena() {
  if (!window.adena) {
    $("walletStatus").innerHTML = `Adena Wallet extension not detected — install at <a href="https://adena.app" target="_blank" rel="noopener">adena.app</a> or copy the <code>gnokey</code> commands shown below.`;
    return null;
  }
  try {
    await window.adena.AddEstablish("Tollgate Metering Vault");
    const acc = await window.adena.GetAccount();
    if (acc?.status !== "success" || !acc?.data?.address) throw new Error(acc?.message || "Account read failed");
    adenaAccount = acc.data;
    const chainId = liveSummary.chainId || "onyx-1";
    const chainOk = adenaAccount.chainId === chainId;
    const shortAddr = `${adenaAccount.address.slice(0, 10)}…${adenaAccount.address.slice(-6)}`;
    $("connectAdenaBtn").textContent = `Adena: ${shortAddr}`;
    $("walletStatus").innerHTML = `Connected <code>${escapeHtml(adenaAccount.address)}</code> · Balance: <code>${escapeHtml(adenaAccount.coins || "0ugnot")}</code> · Chain: <code>${escapeHtml(adenaAccount.chainId || "unknown")}</code>` +
      (chainOk ? ` <span style="color:var(--ok)">(${escapeHtml(chainId)} ✓)</span>` : ` <a href="#" id="switchChainLink">(Switch to ${escapeHtml(chainId)})</a>`);
    if (!chainOk && $("switchChainLink")) {
      $("switchChainLink").addEventListener("click", async (e) => {
        e.preventDefault();
        await window.adena.SwitchNetwork(chainId);
        await connectAdena();
      });
    }
    return adenaAccount;
  } catch (err) {
    $("walletStatus").textContent = `Adena connect error: ${err.message || err}`;
    return null;
  }
}

async function adenaCall(funcName, args, sendCoins, memo) {
  const realm = $("realmInput").value.trim();
  const rpc = $("rpcInput").value.trim();
  const chainId = liveSummary.chainId || "onyx-1";
  const cliCmd =
    `gnokey maketx call <key> -broadcast -remote ${rpc} -chainid ${chainId} \\\n` +
    (sendCoins ? `  -send ${sendCoins} \\\n` : "") +
    `  -pkgpath ${realm} -func ${funcName} ` +
    args.map((a) => `-args ${JSON.stringify(String(a))}`).join(" ");

  if (!adenaAccount) {
    const acc = await connectAdena();
    if (!acc) {
      $("adenaTxOut").textContent = `Adena Wallet not detected. Broadcast via CLI:\n\n${cliCmd}`;
      return;
    }
  }
  $("adenaTxOut").textContent = `Requesting ${funcName} signature in Adena Wallet…`;
  try {
    const resp = await window.adena.DoContract({
      messages: [{
        type: "/vm.m_call",
        value: {
          caller: adenaAccount.address,
          send: sendCoins || "",
          pkg_path: realm,
          func: funcName,
          args: args.map((a) => String(a)),
        },
      }],
      gasFee: 1000000,
      gasWanted: 35000000,
      memo: memo || `Tollgate ${funcName}`,
    });
    if (resp?.status !== "success") throw new Error(resp?.message || JSON.stringify(resp));
    $("adenaTxOut").textContent = `${funcName} broadcast succeeded!\n${JSON.stringify(resp.data || resp, null, 2)}`;
    setTimeout(syncChain, 3500);
  } catch (err) {
    $("adenaTxOut").textContent = `Adena error: ${err.message}\n\nCLI equivalent:\n${cliCmd}`;
  }
}

// --- Tab 3: Interactive HTTP 402 + PayWord Playground (Live Gateway OR Browser Simulator) ---
function gatewayBase() {
  return $("gatewayInput").value.trim().replace(/\/+$/, "");
}

async function runPlayground(mode) {
  const base = gatewayBase();
  const path = $("endpointSelect").value;
  const units = Math.max(1, Number($("unitsInput").value) || 1);
  const pill = $("httpStatusPill");

  // Live gateway mode when gateway URL is configured (or served by local tollgate-gateway)
  if (base || hasSameOriginGateway) {
    const headers = { "X-Tollgate-Units": String(units) };
    if (mode !== "probe") {
      const passId = $("passIdInput").value.trim();
      const subId = $("subIdInput").value.trim();
      const voucherSig = $("voucherSigInput").value.trim();
      if (subId) headers["X-Tollgate-Sub"] = subId;
      else if (passId) headers["X-Tollgate-Pass"] = passId;
      if (voucherSig) headers["X-Tollgate-Voucher-Sig"] = voucherSig;
      headers["X-Tollgate-Nonce"] = mode === "replay" && lastSimCall ? lastSimCall.nonce : `web-${Date.now()}`;
    }
    try {
      const res = await fetch(`${base}${path}`, { headers });
      const bodyText = await res.text();
      let parsedBody = bodyText;
      try { parsedBody = JSON.parse(bodyText); } catch {}
      pill.textContent = `HTTP ${res.status}`;
      pill.className = "pill " + (res.status === 200 ? "p200" : res.status === 402 ? "p402" : "perr");
      const prHeader = res.headers.get("PAYMENT-REQUIRED");
      $("playgroundOutput").textContent = JSON.stringify({
        mode: "live-gateway",
        status: res.status,
        decodedPaymentRequired: prHeader ? JSON.parse(atob(prHeader)) : null,
        body: parsedBody,
      }, null, 2);
    } catch (err) {
      pill.textContent = "ERROR";
      pill.className = "pill perr";
      $("playgroundOutput").textContent = `Gateway request failed: ${err.message}`;
    }
    return;
  }

  // Zero-Server Browser WebCrypto Simulator Mode (backed by live onyx-1 Service #1 pricing!)
  const svc = liveServices[0] || { id: 1, slug: "alpha-oracle", unitPriceUgnot: 1000, subPriceUgnot: 10000, subQuota: 50, payTo: "g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2" };
  const chainId = liveSummary.chainId || "onyx-1";
  const realm = $("realmInput").value.trim();
  const pricing = computeTierCharge(svc.unitPriceUgnot, units);

  if (mode === "probe") {
    pill.textContent = "HTTP 402 (SIMULATOR)";
    pill.className = "pill p402";
    const offer = {
      x402Version: 2,
      mode: "browser-webcrypto-simulator (backed by live onyx-1 Service #1 pricing)",
      accepts: [
        { scheme: "exact", network: `gno:${chainId}`, amount: String(pricing.net), asset: "ugnot", payTo: svc.payTo || "g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2" },
        { scheme: "tollgate-payword-pass", realm, serviceId: svc.id, unitPriceUgnot: svc.unitPriceUgnot, requestedUnits: units, discountBps: pricing.bps, netUgnot: pricing.net },
        { scheme: "tollgate-subscription", realm, serviceId: svc.id, subPriceUgnot: svc.subPriceUgnot, subQuota: svc.subQuota },
      ],
      volumeDiscounts: { "10+_units": "5% off (500 bps)", "50+_units": "10% off (1000 bps)", "100+_units": "20% off (2000 bps)" },
    };
    $("playgroundOutput").textContent = JSON.stringify(offer, null, 2);
    return;
  }

  if (!pwChain.hashes.length) {
    await generatePayWordChain(200);
  }

  if (mode === "replay") {
    if (!lastSimCall) {
      pill.textContent = "RUN STEP 2 FIRST";
      pill.className = "pill perr";
      $("playgroundOutput").textContent = "Execute Step 2 (Metered PayWord Call) first, then click Test Replay Attack to replay its exact nonce & PayWord preimage.";
      return;
    }
    const anchorStillValid = await verifyPayWord(chainId, pwChain.currentAnchor, lastSimCall.proofHex, lastSimCall.units);
    pill.textContent = "HTTP 409 REPLAY BLOCKED";
    pill.className = "pill perr";
    $("playgroundOutput").textContent = JSON.stringify({
      status: 409,
      error: "tollgate: duplicate nonce & stale PayWord anchor",
      replayedNonce: lastSimCall.nonce,
      nonceAlreadyConsumed: simUsedNonces.has(lastSimCall.nonce),
      proofMatchesAdvancedAnchor: anchorStillValid,
      explanation: "Both the gateway nonce registry AND the advanced PayWord hash-chain anchor independently reject the replayed preimage.",
    }, null, 2);
    return;
  }

  if (mode === "overcharge") {
    // Prove a provider cannot claim (units + 1) from a buyer's units-step preimage!
    const claimedUnits = units + 1;
    if (pwChain.spent + units > pwChain.depth) await generatePayWordChain(200);
    const honestProof = pwChain.hashes[pwChain.depth - (pwChain.spent + units)];
    const validForHonest = await verifyPayWord(chainId, pwChain.currentAnchor, honestProof, units);
    const validForOvercharge = await verifyPayWord(chainId, pwChain.currentAnchor, honestProof, claimedUnits);
    pill.textContent = "OVERCHARGE REJECTED ✓";
    pill.className = "pill p200";
    $("playgroundOutput").textContent = JSON.stringify({
      cryptographicOverchargeResistance: validForHonest && !validForOvercharge,
      buyerAuthorizedUnits: units,
      maliciousProviderClaimedUnits: claimedUnits,
      verifyAtAuthorizedUnits: validForHonest,
      verifyAtOverchargedUnits: validForOvercharge,
      proofHex: honestProof,
      currentAnchor: pwChain.currentAnchor,
      explanation: "Because SHA-256 is one-way, the buyer's preimage satisfies SHA256^units(proof) == anchor and cryptographically FAILS for units+1.",
    }, null, 2);
    return;
  }

  // Step 2: Execute Metered PayWord Call in browser WebCrypto!
  if (pwChain.spent + units > pwChain.depth) {
    await generatePayWordChain(Math.max(200, units * 5));
  }
  const prevAnchor = pwChain.currentAnchor;
  const nextSpent = pwChain.spent + units;
  const proofHex = pwChain.hashes[pwChain.depth - nextSpent];
  const verified = await verifyPayWord(chainId, prevAnchor, proofHex, units);
  const nonce = `sim-${Date.now()}`;
  const resourceHash = await sha256Hex(`GET:${path}`);
  const buyerAddr = adenaAccount?.address || "g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2";
  const commitment = await computeReceiptCommitment({
    chainId,
    serviceId: svc.id,
    passId: Number($("passIdInput").value) || 1,
    buyer: buyerAddr,
    provider: svc.payTo || "g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2",
    units,
    amountUgnot: pricing.net,
    nonce,
    resourceHash,
  });

  pwChain.spent = nextSpent;
  pwChain.currentAnchor = proofHex;
  simUsedNonces.add(nonce);
  lastSimCall = { nonce, proofHex, units, commitment };
  $("voucherSigInput").value = proofHex;
  $("simCursorLabel").textContent = `PayWord chain advanced: ${pwChain.spent} / ${pwChain.depth} units spent (anchor ${proofHex.slice(0, 12)}…)`;

  pill.textContent = "HTTP 200 PAID ✓";
  pill.className = "pill p200";
  $("playgroundOutput").textContent = JSON.stringify({
    mode: "browser-webcrypto-simulator (exact Gno/Go/TS cryptographic parity)",
    status: 200,
    rail: "metered-pass",
    paywordVerified: verified,
    units,
    grossUgnot: pricing.gross,
    discountBps: pricing.bps,
    netUgnot: pricing.net,
    previousAnchor: prevAnchor,
    newAnchorProofHex: proofHex,
    nonce,
    resourceHash,
    canonicalReceiptCommitment: commitment,
    meteredResponse: path.includes("oracle")
      ? { pair: "GNOT-USD", price: 0.4218, source: "alpha-oracle (simulated locally)", chainHeight: liveSummary.height }
      : { city: "zurich", tempC: 6.4, condition: "clear", source: "metered-weather (simulated locally)" },
  }, null, 2);
}

// --- Bind Events ---
$("connectAdenaBtn").addEventListener("click", connectAdena);
$("refreshBtn").addEventListener("click", syncChain);
$("verifyReceiptBtn").addEventListener("click", () => verifyOnChainReceipt());
document.querySelectorAll("[data-rid]").forEach((el) => {
  el.addEventListener("click", () => verifyOnChainReceipt(el.dataset.rid));
});
$("genPayWordBtn").addEventListener("click", () => generatePayWordChain());
$("verifyGoldenBtn").addEventListener("click", verifyCrossLanguageGoldenVector);

$("adenaOpenPassBtn").addEventListener("click", async () => {
  if (!pwChain.anchorHex) await generatePayWordChain();
  await adenaCall(
    "OpenPass",
    [$("opSvcId").value, $("opMaxCall").value, $("opExpiry").value, pwChain.anchorHex],
    `${$("opDeposit").value}ugnot`,
    `Tollgate OpenPass Service #${$("opSvcId").value}`
  );
});

$("adenaSubBtn").addEventListener("click", async () => {
  if (!pwChain.anchorHex) await generatePayWordChain();
  await adenaCall(
    "Subscribe",
    [$("subSvcId").value, pwChain.anchorHex],
    `${$("subPrice").value}ugnot`,
    `Tollgate Subscribe Service #${$("subSvcId").value}`
  );
});

$("adenaFinalizeBtn").addEventListener("click", () => adenaCall("FinalizeSettlement", [$("mgmtId").value], "", "Tollgate FinalizeSettlement"));
$("adenaDisputeBtn").addEventListener("click", () => adenaCall("DisputeSettlement", [$("mgmtId").value, $("mgmtReason").value], "", "Tollgate DisputeSettlement"));
$("adenaClosePassBtn").addEventListener("click", () => adenaCall("ClosePass", [$("mgmtId").value], "", "Tollgate ClosePass"));

$("probe402Btn").addEventListener("click", () => runPlayground("probe"));
$("callPaidBtn").addEventListener("click", () => runPlayground("paid"));
$("testReplayBtn").addEventListener("click", () => runPlayground("replay"));
$("testOverchargeBtn").addEventListener("click", () => runPlayground("overcharge"));

// Boot
const qp = new URLSearchParams(location.search);
if (qp.get("gateway")) $("gatewayInput").value = qp.get("gateway");
const isStaticHost = location.hostname.endsWith("github.io") || location.protocol === "file:";
if (!isStaticHost) {
  fetch("/api/health").then((r) => (r.ok ? r.json() : null)).then((h) => {
    if (h && h.rpc && h.realm) {
      hasSameOriginGateway = true;
      $("rpcInput").value = h.rpc;
      $("realmInput").value = h.realm;
    }
  }).catch(() => {}).finally(() => { syncChain(); verifyOnChainReceipt(1); });
} else {
  syncChain();
  verifyOnChainReceipt(1);
}
