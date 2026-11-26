#!/usr/bin/env bash
set -euo pipefail

BIN="${GNO_BIN_DIR:-/home/ubuntu/gnome-tavern/bin}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${TOLLGATE_ADDR:-$(cat /home/ubuntu/gnome-tavern/keys/address.txt)}"
KEYHOME="/home/ubuntu/gnome-tavern/keys/gnokey_home"
PW="$(cat /home/ubuntu/gnome-tavern/keys/.pw)"
RPC="https://rpc.onyx.testnets.gno.land:443"
CHAIN_ID="onyx-1"
R_PATH="gno.land/r/${ADDR}/tollgate/v2"

echo "=== 1. Verify /r/tollgate/v2 is live on onyx-1 RPC ==="
"$BIN/gnokey" query "vm/qfile" -data "$R_PATH" -remote "$RPC"

echo "=== 2. Generate SEPARATE PayWord SHA-256 chains for pass & subscription ==="
PASS_KP=$("$ROOT/bin/tollgate-agent" keygen --chain-id "$CHAIN_ID" --depth 1000)
PASS_ANCHOR=$(echo "$PASS_KP" | python3 -c "import sys,json; print(json.load(sys.stdin)[anchorHex])")
PASS_SEED=$(echo "$PASS_KP" | python3 -c "import sys,json; print(json.load(sys.stdin)[seedHex])")

SUB_KP=$("$ROOT/bin/tollgate-agent" keygen --chain-id "$CHAIN_ID" --depth 1000)
SUB_ANCHOR=$(echo "$SUB_KP" | python3 -c "import sys,json; print(json.load(sys.stdin)[anchorHex])")
SUB_SEED=$(echo "$SUB_KP" | python3 -c "import sys,json; print(json.load(sys.stdin)[seedHex])")

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func RegisterService \
  -args "alpha-oracle" \
  -args "Alpha Price Oracle" \
  -args "https://mevlanayalcin.github.io/tollgate/" \
  -args "$ADDR" \
  -args "1000" -args "10000" -args "5000" -args "100" -args "20" \
  -args "Real-time verifiable market quotes over HTTP 402 (gnolang/x402) and Tollgate PayWord SHA-256 credit passes."
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 50000ugnot \
  -pkgpath "$R_PATH" -func OpenPass \
  -args "1" -args "2000" -args "5000" -args "$PASS_ANCHOR"
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 10000ugnot \
  -pkgpath "$R_PATH" -func Subscribe \
  -args "1" -args "$SUB_ANCHOR"
sleep 5

RES_HASH=$(python3 -c "import hashlib; print(hashlib.sha256(b'GET /api/v1/oracle?pair=GNOT-USD').hexdigest())")
PASS_PROOF=$("$ROOT/bin/tollgate-agent" payword-proof --seed "$PASS_SEED" --chain-id "$CHAIN_ID" --depth 1000 --anchor "$PASS_ANCHOR" --units 10)
SUB_PROOF=$("$ROOT/bin/tollgate-agent" payword-proof --seed "$SUB_SEED" --chain-id "$CHAIN_ID" --depth 1000 --anchor "$SUB_ANCHOR" --units 2)

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func ClaimSettlement \
  -args "1" -args "10" -args "onyx-pass-batch-001" -args "$RES_HASH" -args "$PASS_PROOF"
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func ConsumeSubscription \
  -args "1" -args "2" -args "onyx-sub-batch-001" -args "$RES_HASH" -args "$SUB_PROOF"
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func RecordX402Receipt \
  -args "1" -args "$ADDR" -args "1" -args "1000" -args "onyx-x402-anchor-001" -args "$RES_HASH" -args "x402-onyx-facilitator-demo-001"
sleep 5

"$BIN/gnokey" query "vm/qrender" -data "${R_PATH}:json/summary" -remote "$RPC"
echo "=== TOLLGATE/V2 SEEDED & LIVE ON ONYX-1 ==="
echo "gnoweb: https://onyx.testnets.gno.land/r/${ADDR}/tollgate/v2"
