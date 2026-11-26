const $ = (id) => document.getElementById(id);

function toHexUtf8(str) {
  const bytes = new TextEncoder().encode(str);
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function queryRender(rpcUrl, realmPath, subPath) {
  const cleanRpc = rpcUrl.replace(/\/+$/, "");
  const cleanRealm = realmPath.replace(/^\/+|\/+$/g, "");
  const dataHex = toHexUtf8(`${cleanRealm}:${subPath}`);
  const url = `${cleanRpc}/abci_query?path=${encodeURIComponent('"vm/qrender"')}&data=0x${dataHex}`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`RPC HTTP ${res.status}`);
  const env = await res.json();
  const base = env?.result?.response?.ResponseBase;
  if (base?.Error) throw new Error(JSON.stringify(base.Error));
  if (!base?.Data) return "";
  const bin = atob(base.Data);
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

function lp(s) {
  const byteLen = new TextEncoder().encode(s).length;
  return `${byteLen}:${s}|`;
}

async function sha256Hex(str) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(str));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function computeReceiptCommitment(p) {
  const preimage =
    `tollgate-receipt-v1|` +
    lp(p.chainId) +
    `${p.serviceId}|${p.passId}|` +
    lp(p.buyer) +
    lp(p.provider) +
    `${p.units}|${p.amountUgnot}|` +
    lp(p.nonce) +
    lp(p.resourceHash);
  return sha256Hex(preimage);
}

async function syncChain() {
  const rpc = $("rpcInput").value.trim();
  const realm = $("realmInput").value.trim();
  $("statusBadge").textContent = "RPC: syncing…";
  try {
    const [sumRaw, svcRaw] = await Promise.all([
      queryRender(rpc, realm, "json/summary"),
      queryRender(rpc, realm, "json/services"),
    ]);
    const sum = JSON.parse(sumRaw);
    const svcs = JSON.parse(svcRaw);

    $("chainBadge").textContent = `chain: ${sum.chainId}`;
    $("statusBadge").textContent = `RPC: live @ #${sum.height}`;
    $("statTvl").textContent = `${sum.vaultBalanceUgnot} ugnot`;
    $("statHeight").textContent = `#${sum.height}`;
    $("statServices").textContent = `${sum.services}`;
    $("statPasses").textContent = `${sum.passes}`;
    $("statSubs").textContent = `${sum.subscriptions}`;
    $("statReceipts").textContent = `${sum.receipts}`;

    const tbody = $("servicesBody");
    if (!svcs.length) {
      tbody.innerHTML = `<tr><td colspan="8" class="muted">No services registered on this realm yet.</td></tr>`;
    } else {
      tbody.innerHTML = svcs.map((s) => `
        <tr>
          <td>#${s.id}</td>
          <td>${escapeHtml(s.slug)}</td>
          <td>${escapeHtml(s.name)}</td>
          <td>${s.unitPriceUgnot} ugnot</td>
          <td>${s.subPriceUgnot > 0 ? `${s.subPriceUgnot} ugnot / ${s.subQuota}` : "—"}</td>
          <td>${s.challengeBlocks} blk</td>
          <td>${s.totalCalls}</td>
          <td>${s.totalRevenue} ugnot</td>
        </tr>`).join("");
    }
  } catch (err) {
    $("statusBadge").textContent = "RPC: offline / not deployed yet";
    $("servicesBody").innerHTML = `<tr><td colspan="8" class="muted">${escapeHtml(err.message)}</td></tr>`;
  }
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (m) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[m]));
}

function gatewayBase() {
  return $("gatewayInput").value.trim().replace(/\/+$/, "");
}

async function runRequest(useCredentials) {
  const path = $("endpointSelect").value;
  const units = $("unitsInput").value || "1";
  const headers = { "X-Tollgate-Units": String(units) };
  if (useCredentials) {
    const passId = $("passIdInput").value.trim();
    const subId = $("subIdInput").value.trim();
    if (subId) headers["X-Tollgate-Sub"] = subId;
    else if (passId) headers["X-Tollgate-Pass"] = passId;
    headers["X-Tollgate-Nonce"] = `web-${Date.now()}`;
  }
  const url = `${gatewayBase()}${path}`;
  const pill = $("httpStatusPill");
  try {
    const res = await fetch(url, { headers });
    const bodyText = await res.text();
    let parsedBody = bodyText;
    try { parsedBody = JSON.parse(bodyText); } catch {}
    pill.textContent = `HTTP ${res.status}`;
    pill.className = "pill " + (res.status === 200 ? "p200" : res.status === 402 ? "p402" : "perr");

    const prHeader = res.headers.get("PAYMENT-REQUIRED");
    const out = {
      status: res.status,
      tollgateHeaders: {
        rail: res.headers.get("X-Tollgate-Rail"),
        serviceId: res.headers.get("X-Tollgate-Service-Id"),
        realm: res.headers.get("X-Tollgate-Realm"),
        nonce: res.headers.get("X-Tollgate-Nonce"),
        netUgnot: res.headers.get("X-Tollgate-Net-Ugnot"),
        resourceHash: res.headers.get("X-Tollgate-Resource-Hash"),
        receiptCommitment: res.headers.get("X-Tollgate-Receipt-Commitment"),
      },
      decodedPaymentRequired: prHeader ? JSON.parse(atob(prHeader)) : null,
      body: parsedBody,
    };
    $("playgroundOutput").textContent = JSON.stringify(out, null, 2);
  } catch (err) {
    pill.textContent = "ERROR";
    pill.className = "pill perr";
    $("playgroundOutput").textContent = `Gateway request failed: ${err.message}\nStart local gateway with: ./bin/tollgate-gateway --realm <realm> --pay-to <addr>`;
  }
}

async function verifyOnChainReceipt() {
  const rpc = $("rpcInput").value.trim();
  const realm = $("realmInput").value.trim();
  const q = $("lookupReceiptInput").value.trim();
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
    $("verifierOutput").textContent = JSON.stringify({
      verifiedByWebCrypto: recomputed === rec.commitment,
      recomputedSha256: recomputed,
      onChainReceipt: rec,
    }, null, 2);
  } catch (err) {
    $("verifierOutput").textContent = `Verification error: ${err.message}`;
  }
}

$("refreshBtn").addEventListener("click", syncChain);
$("probe402Btn").addEventListener("click", () => runRequest(false));
$("callPaidBtn").addEventListener("click", () => runRequest(true));
$("verifyReceiptBtn").addEventListener("click", verifyOnChainReceipt);

// Auto-detect local gateway health if served directly by tollgate-gateway
fetch("/api/health").then((r) => r.ok ? r.json() : null).then((h) => {
  if (h && h.rpc && h.realm) {
    $("rpcInput").value = h.rpc;
    $("realmInput").value = h.realm;
  }
}).catch(() => {}).finally(syncChain);
