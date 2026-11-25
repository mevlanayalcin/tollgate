#!/usr/bin/env bash
set -euo pipefail

GNOROOT="${GNOROOT:-/home/ubuntu/gnome-tavern/gno}"
BIN="${GNO_BIN_DIR:-/home/ubuntu/gnome-tavern/bin}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${TOLLGATE_ADDR:-$(cat /home/ubuntu/gnome-tavern/keys/address.txt)}"
KEYHOME="/home/ubuntu/gnome-tavern/keys/gnokey_home"
PW="$(cat /home/ubuntu/gnome-tavern/keys/.pw)"

RPC_PORT=36657
WEB_PORT=38888
GW_PORT=38402
RPC_URL="http://127.0.0.1:${RPC_PORT}"
P_PATH="gno.land/p/${ADDR}/tollgate"
R_PATH="gno.land/r/${ADDR}/tollgate"

cleanup() {
  set +e
  [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null || true
  [ -n "${GNODEV_PID:-}" ] && kill "$GNODEV_PID" 2>/dev/null || true
}
trap cleanup EXIT

wait_next_block() {
  local h0 h1
  h0=$(curl -sS "${RPC_URL}/status" | python3 -c "import sys,json; print(int(json.load(sys.stdin)['result']['sync_info']['latest_block_height']))")
  for _ in $(seq 1 20); do
    sleep 0.35
    h1=$(curl -sS "${RPC_URL}/status" | python3 -c "import sys,json; print(int(json.load(sys.stdin)['result']['sync_info']['latest_block_height']))")
    if [ "$h1" -gt "$h0" ]; then return 0; fi
  done
}

echo "[1/7] Starting isolated gnodev v1.8.0-rc.1 on ${RPC_URL} (funding ${ADDR})..."
GNOROOT="$GNOROOT" "$BIN/gnodev" local \
  -C /tmp \
  -root "$GNOROOT" \
  -paths "gno.land/p/nt/avl/v0,gno.land/p/nt/ufmt/v0,gno.land/p/nt/seqid/v0,gno.land/p/nt/markdown/sanitize/v0,gno.land/p/nt/uassert/v0,gno.land/p/nt/urequire/v0,gno.land/p/nt/testutils/v0" \
  -node-rpc-listener "127.0.0.1:${RPC_PORT}" \
  -web-listener "127.0.0.1:${WEB_PORT}" \
  -add-account "${ADDR}=1000000000ugnot" \
  -no-watch \
  -no-replay \
  -empty-blocks \
  > /tmp/tollgate-gnodev.log 2>&1 &
GNODEV_PID=$!

for i in $(seq 1 90); do
  if curl -sS "${RPC_URL}/status" 2>/dev/null | grep -q '"latest_block_height"'; then
    break
  fi
  sleep 0.5
done
curl -sS "${RPC_URL}/status" | grep -q '"network"' || { echo "gnodev failed to start"; cat /tmp/tollgate-gnodev.log; exit 1; }
CHAIN_ID=$(curl -sS "${RPC_URL}/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['node_info']['network'])")
echo "    gnodev ready: chain_id=${CHAIN_ID}"

echo "[2/7] Deploying /p/ first, then /r/ (exact onyx-1 dependency order)..."
printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 40000000 -max-deposit 50000000ugnot \
  -pkgpath "$P_PATH" -pkgdir "$ROOT/gno/p/${ADDR}/tollgate"
wait_next_block

printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 120000000 -max-deposit 100000000ugnot \
  -pkgpath "$R_PATH" -pkgdir "$ROOT/gno/r/${ADDR}/tollgate"
wait_next_block

echo "[3/7] Calling RegisterService, OpenPass (-send 50000ugnot), Subscribe (-send 10000ugnot)..."
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
  -args "1" -args "2000" -args "500"
wait_next_block

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 10000ugnot \
  -pkgpath "$R_PATH" -func Subscribe \
  -args "1"
wait_next_block

echo "[4/7] Starting live tollgate-gateway backed by gnodev..."
"$ROOT/bin/tollgate-gateway" \
  --listen "127.0.0.1:${GW_PORT}" \
  --rpc "$RPC_URL" \
  --realm "$R_PATH" \
  --chain-id "$CHAIN_ID" \
  --pay-to "$ADDR" \
  --service-id 1 \
  --unit-price 1000 \
  --web "$ROOT/web" \
  > /tmp/tollgate-gw.log 2>&1 &
GW_PID=$!

for i in $(seq 1 20); do
  if curl -sS "http://127.0.0.1:${GW_PORT}/api/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

echo "[5/7] Running tollgate-agent (402 probe -> metered-pass rail -> local SHA-256 proof)..."
PASS_OUT=$("$ROOT/bin/tollgate-agent" call \
  --url "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD" \
  --rpc "$RPC_URL" --realm "$R_PATH" --pass 1 --units 10 --nonce "e2e-pass-001")
echo "$PASS_OUT"
echo "$PASS_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['probeStatus']==402 and d['finalStatus']==200 and d['selectedRail']=='metered-pass' and d['netUgnot']==9500 and d['localProofVerified']==True, d"

echo "[6/7] Running tollgate-agent on subscription rail + MCP stdio protocol check..."
SUB_OUT=$("$ROOT/bin/tollgate-agent" call \
  --url "http://127.0.0.1:${GW_PORT}/api/v1/oracle?pair=GNOT-USD" \
  --rpc "$RPC_URL" --realm "$R_PATH" --sub 1 --units 2 --nonce "e2e-sub-001")
echo "$SUB_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['probeStatus']==402 and d['finalStatus']==200 and d['selectedRail']=='subscription' and d['localProofVerified']==True, d"

MCP_OUT=$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tollgate_chain_summary","arguments":{}}}\n' \
  | "$ROOT/bin/tollgate-agent" mcp --rpc "$RPC_URL" --realm "$R_PATH")
echo "$MCP_OUT" | grep -q "tollgate_paid_fetch"
echo "$MCP_OUT" | python3 -c "import sys,json; lines=[json.loads(l) for l in sys.stdin if l.strip()]; d=json.loads(lines[1]['result']['content'][0]['text']); assert d['services']==1 and d['vaultBalanceUgnot']==50000, d"

echo "[7/7] Settling metered batch on-chain via ClaimSettlement & verifying identical SHA-256 commitment..."
RES_HASH=$(echo "$PASS_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['resourceHash'])")
EXPECTED_COMMIT=$(echo "$PASS_OUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['receiptCommitment'])")

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC_URL" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func ClaimSettlement \
  -args "1" -args "10" -args "e2e-pass-001" -args "$RES_HASH"
wait_next_block

VERIFY_OUT=$("$ROOT/bin/tollgate-agent" verify --rpc "$RPC_URL" --realm "$R_PATH" --receipt "$EXPECTED_COMMIT")
echo "$VERIFY_OUT"
echo "$VERIFY_OUT" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['verified']==True and d['onChainReceipt']['commitment']=='${EXPECTED_COMMIT}', d"

echo "=== TOLLGATE E2E GNODEV 100% GREEN ==="
