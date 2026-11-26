#!/usr/bin/env bash
set -euo pipefail

BIN="${GNO_BIN_DIR:-/home/ubuntu/gnome-tavern/bin}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ADDR="${TOLLGATE_ADDR:-$(cat /home/ubuntu/gnome-tavern/keys/address.txt)}"
KEYHOME="/home/ubuntu/gnome-tavern/keys/gnokey_home"
PW="$(cat /home/ubuntu/gnome-tavern/keys/.pw)"
RPC="https://rpc.onyx.testnets.gno.land:443"
CHAIN_ID="onyx-1"
P_PATH="gno.land/p/${ADDR}/tollgate"
R_PATH="gno.land/r/${ADDR}/tollgate"

wait_gpas_passed() {
  local pkg="$1"
  echo "Checking live vm/qfile on ${pkg} (https://gpas.onyx.testnets.gno.land/?path=${pkg}) ..."
  for _ in $(seq 1 30); do
    if "$BIN/gnokey" query "vm/qfile" -data "$pkg" -remote "$RPC" >/dev/null 2>&1; then
      echo "Package ${pkg} confirmed live on onyx-1 RPC!"
      return 0
    fi
    HTML=$(curl -sS "https://gpas.onyx.testnets.gno.land/?path=${pkg}" || true)
    if echo "$HTML" | grep -qi "failed\|rejected"; then
      echo "ERROR: GPAS rejected ${pkg}!"
      exit 1
    fi
    if echo "$HTML" | grep -qi "enabling at"; then
      echo "Package ${pkg} passed GPAS static check and entered 60-minute activation timelock."
      return 2
    fi
    sleep 3
  done
  return 2
}

echo "=== 1. Check onyx-1 balance ==="
"$BIN/gnokey" query "bank/balances/${ADDR}" -remote "$RPC"

echo "=== 2. Deploy /p/ package if not live ==="
if ! "$BIN/gnokey" query "vm/qfile" -data "$P_PATH" -remote "$RPC" >/dev/null 2>&1; then
  printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
    -home "$KEYHOME" -insecure-password-stdin \
    -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
    -gas-fee 1000000ugnot -gas-wanted 40000000 -max-deposit 50000000ugnot \
    -pkgpath "$P_PATH" -pkgdir "$ROOT/gno/p/${ADDR}/tollgate"
  wait_gpas_passed "$P_PATH" || { echo "Re-run after /p/ activation completes."; exit 0; }
fi

echo "=== 3. Deploy /r/ realm ONLY after /p/ is live ==="
if ! "$BIN/gnokey" query "vm/qfile" -data "$R_PATH" -remote "$RPC" >/dev/null 2>&1; then
  printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
    -home "$KEYHOME" -insecure-password-stdin \
    -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
    -gas-fee 1000000ugnot -gas-wanted 140000000 -max-deposit 120000000ugnot \
    -pkgpath "$R_PATH" -pkgdir "$ROOT/gno/r/${ADDR}/tollgate"
  wait_gpas_passed "$R_PATH" || { echo "Re-run after /r/ activation completes."; exit 0; }
fi

echo "=== 4. Seed live demo service, Ed25519-authenticated pass, subscription, and verified voucher receipt ==="
KP_JSON=$("$ROOT/bin/tollgate-agent" keygen)
VOUCHER_PUB=$(echo "$KP_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['publicKeyHex'])")
VOUCHER_SEED=$(echo "$KP_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['seedHex'])")

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
  -args "Real-time verifiable market quotes over HTTP 402 (gnolang/x402) and Tollgate Ed25519 credit passes."
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 50000ugnot \
  -pkgpath "$R_PATH" -func OpenPass \
  -args "1" -args "2000" -args "5000" -args "$VOUCHER_PUB"
sleep 5

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 10000ugnot \
  -pkgpath "$R_PATH" -func Subscribe \
  -args "1" -args "$VOUCHER_PUB"
sleep 5

RES_HASH=$(python3 -c "import hashlib; print(hashlib.sha256(b'GET /api/v1/oracle?pair=GNOT-USD').hexdigest())")
SIG_HEX=$("$ROOT/bin/tollgate-agent" sign-voucher \
  --voucher-key "$VOUCHER_SEED" \
  --chain-id "$CHAIN_ID" \
  --service 1 --pass 1 --sub 0 --units 10 \
  --nonce "onyx-demo-batch-001" --res-hash "$RES_HASH")

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func ClaimSettlement \
  -args "1" -args "10" -args "onyx-demo-batch-001" -args "$RES_HASH" -args "$SIG_HEX"
sleep 5

"$BIN/gnokey" query "vm/qrender" -data "${R_PATH}:json/summary" -remote "$RPC"
echo "=== TOLLGATE LIVE ON ONYX-1 ==="
echo "gnoweb: https://onyx.testnets.gno.land/r/${ADDR}/tollgate"
