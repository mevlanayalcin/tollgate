#!/usr/bin/env bash
set -euo pipefail

BIN="${GNO_BIN_DIR:-/home/ubuntu/gnome-tavern/bin}"
GNOROOT="${GNOROOT:-/home/ubuntu/gnome-tavern/gno}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${TOLLGATE_ADDR:-$(cat /home/ubuntu/gnome-tavern/keys/address.txt)}"
KEYHOME="/home/ubuntu/gnome-tavern/keys/gnokey_home"
PW_FILE="/home/ubuntu/gnome-tavern/keys/.pw"
PW="$(cat "$PW_FILE")"
RPC_PORT=36657
WEB_PORT=38888
GW_PORT=38402
RPC_URL="http://127.0.0.1:${RPC_PORT}"
CHAIN_ID="dev"
P_PATH="gno.land/p/${ADDR}/tollgate"
R_PATH="gno.land/r/${ADDR}/tollgate"
STATE_FILE="/tmp/tollgate-e2e-state.json"

cleanup() {
  if [[ -n "${GW_PID:-}" ]]; then kill "$GW_PID" 2>/dev/null || true; fi
  if [[ -n "${GNODEV_PID:-}" ]]; then kill "$GNODEV_PID" 2>/dev/null || true; fi
  rm -f "$STATE_FILE" "${STATE_FILE}.tmp"
}
trap cleanup EXIT
rm -f "$STATE_FILE" "${STATE_FILE}.tmp"

wait_next_block() {
  local h1 h2
  h1=$(curl -sS "${RPC_URL}/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])")
  for _ in $(seq 1 20); do
    sleep 0.3
    h2=$(curl -sS "${RPC_URL}/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])")
    if [[ "$h2" != "$h1" ]]; then
      return 0
    fi
  done
}

echo "[1/8] Starting local gnodev (v1.8.0-rc.1)..."
"$BIN/gnodev" local \
  -C /tmp \
  -root "$GNOROOT" \
  -paths "gno.land/p/nt/avl/v0,gno.land/p/nt/ufmt/v0,gno.land/p/nt/seqid/v0,gno.land/p/nt/markdown/sanitize/v0,gno.land/p/nt/uassert/v0,gno.land/p/nt/urequire/v0,gno.land/p/nt/testutils/v0" \
  -node-rpc-listener "127.0.0.1:${RPC_PORT}" \
  -web-listener "127.0.0.1:${WEB_PORT}" \
  -add-account "${ADDR}=500000000ugnot" \
  -no-watch -no-replay -empty-blocks \
  > /tmp/tollgate-gnodev.log 2>&1 &
GNODEV_PID=$!

for _ in $(seq 1 90); do
  if curl -sS "${RPC_URL}/status" 2>/dev/null | grep -q '"latest_block_height"'; then
    break
  fi
  sleep 0.5
done
curl -sS "${RPC_URL}/status" | grep -q "latest_block_height"

echo "[2/8] Deploying /p/ and /r/ tollgate packages to gnodev..."
printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 40000000 -max-deposit 50000000ugnot \
  -pkgpath "$P_PATH" -pkgdir "$ROOT/gno/p/${ADDR}/tollgate"
wait_next_block

printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 140000000 -max-deposit 120000000ugnot \
  -pkgpath "$R_PATH" -pkgdir "$ROOT/gno/r/${ADDR}/tollgate"
wait_next_block

echo "[3/8] Generating Ed25519 buyer voucher keypair & calling RegisterService, OpenPass, Subscribe..."
KP_JSON=$("$ROOT/bin/tollgate-agent" keygen)
VOUCHER_PUB=$(echo "$KP_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['publicKeyHex'])")
VOUCHER_SEED=$(echo "$KP_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['seedHex'])")

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func RegisterService \
  -args "alpha-oracle" \
  -args "Alpha Price Oracle" \
  -args "http://127.0.0.1:${GW_PORT}/api/v1/oracle" \
  -args "$ADDR" \
  -args "1000" -args "10000" -args "500" -args "50" -args "10" \
  -args "Live verified market feeds over HTTP 402 + Tollgate."
wait_next_block

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 50000ugnot \
  -pkgpath "$R_PATH" -func OpenPass \
  -args "1" -args "2000" -args "500" -args "$VOUCHER_PUB"
wait_next_block

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 10000ugnot \
  -pkgpath "$R_PATH" -func Subscribe \
  -args "1" -args "$VOUCHER_PUB"
wait_next_block

echo "[4/8] Starting live tollgate-gateway with Ed25519 auth, persistent state, and on-chain GnokeySubmitter..."
"$ROOT/bin/tollgate-gateway" \
  --listen "127.0.0.1:${GW_PORT}" \
  --rpc "$RPC_URL" \
  --realm "$R_PATH" \
  --chain-id "$CHAIN_ID" \
  --pay-to "$ADDR" \
  --service-id 1 \
  --unit-price 1000 \
  --web "$ROOT/web" \
  --state-file "$STATE_FILE" \
  --require-auth=true --auto-settle=0 \
  --gnokey-bin "$BIN/gnokey" \
  --key-name deployer \
  --key-home "$KEYHOME" \
  --key-pass-file "$PW_FILE" \
  > /tmp/tollgate-gw.log 2>&1 &
GW_PID=$!

for _ in $(seq 1 20); do
  if curl -sS "http://127.0.0.1:${GW_PORT}/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

echo "[5/8] Verifying unsigned pass request is rejected (HTTP 401)..."
UNAUTH_CODE=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "X-Tollgate-Pass: 1" -H "X-Tollgate-Units: 1" -H "X-Tollgate-Nonce: unsigned-attempt" \
  "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD")
if [[ "$UNAUTH_CODE" != "401" ]]; then
  echo "Expected HTTP 401 on unsigned pass call, got $UNAUTH_CODE"
  exit 1
fi
echo "Unsigned request rejected cleanly with HTTP 401."

echo "[6/8] Running tollgate-agent with Ed25519 voucher signing on pass & subscription rails + replay check..."
PASS_OUT=$("$ROOT/bin/tollgate-agent" call \
  --url "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD" \
  --rpc "$RPC_URL" --realm "$R_PATH" --pass 1 --units 10 --nonce "e2e-pass-001" \
  --voucher-key "$VOUCHER_SEED")
echo "$PASS_OUT"
echo "$PASS_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['probeStatus']==402 and d['finalStatus']==200 and d['selectedRail']=='metered-pass' and d['netUgnot']==9500 and d['localProofVerified']==True and len(d['voucherSigHex'])==128, d"

REPLAY_SIG=$(echo "$PASS_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['voucherSigHex'])")
REPLAY_CODE=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "X-Tollgate-Pass: 1" -H "X-Tollgate-Units: 10" -H "X-Tollgate-Nonce: e2e-pass-001" \
  -H "X-Tollgate-Voucher-Sig: ${REPLAY_SIG}" \
  "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD")
if [[ "$REPLAY_CODE" != "409" ]]; then
  echo "Expected HTTP 409 on nonce replay, got $REPLAY_CODE"
  exit 1
fi
echo "Nonce replay rejected cleanly with HTTP 409."

SUB_OUT=$("$ROOT/bin/tollgate-agent" call \
  --url "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD" \
  --rpc "$RPC_URL" --realm "$R_PATH" --sub 1 --units 2 --nonce "e2e-sub-001" \
  --voucher-key "$VOUCHER_SEED")
echo "$SUB_OUT"
echo "$SUB_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['probeStatus']==402 and d['finalStatus']==200 and d['selectedRail']=='subscription' and d['localProofVerified']==True and len(d['voucherSigHex'])==128, d"

echo "[7/8] Checking MCP stdio server & triggering real gateway on-chain settlement flush (/api/settle-now)..."
MCP_OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tollgate_chain_summary","arguments":{}}}\n' \
  | "$ROOT/bin/tollgate-agent" mcp --rpc "$RPC_URL" --realm "$R_PATH" --voucher-key "$VOUCHER_SEED")
echo "$MCP_OUT" | grep -q "tollgate_paid_fetch"
echo "$MCP_OUT" | python3 -c "import sys,json; lines=[json.loads(l) for l in sys.stdin if l.strip()]; d=json.loads(lines[1]['result']['content'][0]['text']); assert d['services']==1 and d['vaultBalanceUgnot']==50000, d"

SETTLE_OUT=$(curl -sS -X POST "http://127.0.0.1:${GW_PORT}/api/settle-now")
echo "$SETTLE_OUT"
echo "$SETTLE_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['settled']==2 and all(r['settled'] for r in d['receipts']), d"
wait_next_block

echo "[8/8] Verifying BOTH gateway-settled Ed25519 receipts on-chain & checking gnoweb rendering..."
EXPECTED_PASS_COMMIT=$(echo "$PASS_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['receiptCommitment'])")
EXPECTED_SUB_COMMIT=$(echo "$SUB_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['receiptCommitment'])")

VERIFY_PASS=$("$ROOT/bin/tollgate-agent" verify --rpc "$RPC_URL" --realm "$R_PATH" --receipt "$EXPECTED_PASS_COMMIT")
echo "$VERIFY_PASS"
echo "$VERIFY_PASS" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['verified']==True and d['onChainReceipt']['buyerSigned']==True and d['onChainReceipt']['commitment']=='${EXPECTED_PASS_COMMIT}', d"

VERIFY_SUB=$("$ROOT/bin/tollgate-agent" verify --rpc "$RPC_URL" --realm "$R_PATH" --receipt "$EXPECTED_SUB_COMMIT")
echo "$VERIFY_SUB"
echo "$VERIFY_SUB" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['verified']==True and d['onChainReceipt']['buyerSigned']==True and d['onChainReceipt']['commitment']=='${EXPECTED_SUB_COMMIT}', d"

GNOWEB_HTML=$(curl -sS "http://127.0.0.1:${WEB_PORT}/r/${ADDR}/tollgate")
echo "$GNOWEB_HTML" | grep -q "Alpha Price Oracle"
if echo "$GNOWEB_HTML" | grep -q "GnoLinkTypeInvalid\|data-invalid"; then
  echo "Detected invalid gnoweb link!"
  exit 1
fi

echo "=== TOLLGATE E2E GNODEV 100% GREEN (ED25519 AUTH + AUTO-SETTLE + GNOWEB VERIFIED) ==="
