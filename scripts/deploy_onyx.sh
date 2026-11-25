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
  echo "Waiting for GPAS approval on https://gpas.onyx.testnets.gno.land/?path=${pkg} ..."
  for i in $(seq 1 90); do
    HTML=$(curl -sS "https://gpas.onyx.testnets.gno.land/?path=${pkg}" || true)
    if echo "$HTML" | grep -qi "passed" && echo "$HTML" | grep -qiE "yes|active|enabled|on chain"; then
      echo "GPAS PASSED & ON-CHAIN for ${pkg}!"
      return 0
    fi
    if echo "$HTML" | grep -qi "failed\|rejected"; then
      echo "ERROR: GPAS rejected ${pkg}!"
      echo "$HTML" | grep -oE '<pre[^>]*>.*</pre>' | head -c 800 || true
      exit 1
    fi
    # Also verify directly via vm/qfile on onyx-1 RPC
    if "$BIN/gnokey" query "vm/qfile" -data "$pkg" -remote "$RPC" >/dev/null 2>&1; then
      echo "Package ${pkg} confirmed live on onyx-1 RPC!"
      return 0
    fi
    sleep 5
  done
  echo "Timed out waiting for GPAS on ${pkg}"
  exit 1
}

echo "=== 1. Check onyx-1 balance ==="
"$BIN/gnokey" query "bank/balances/${ADDR}" -remote "$RPC"

echo "=== 2. Deploy /p/ package first ==="
if ! "$BIN/gnokey" query "vm/qfile" -data "$P_PATH" -remote "$RPC" >/dev/null 2>&1; then
  printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
    -home "$KEYHOME" -insecure-password-stdin \
    -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
    -gas-fee 1000000ugnot -gas-wanted 40000000 -max-deposit 50000000ugnot \
    -pkgpath "$P_PATH" -pkgdir "$ROOT/gno/p/${ADDR}/tollgate"
fi
wait_gpas_passed "$P_PATH"

echo "=== 3. Deploy /r/ realm ONLY after /p/ is passed & live ==="
if ! "$BIN/gnokey" query "vm/qfile" -data "$R_PATH" -remote "$RPC" >/dev/null 2>&1; then
  printf "%s\n" "$PW" | "$BIN/gnokey" maketx addpkg deployer \
    -home "$KEYHOME" -insecure-password-stdin \
    -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
    -gas-fee 1000000ugnot -gas-wanted 120000000 -max-deposit 100000000ugnot \
    -pkgpath "$R_PATH" -pkgdir "$ROOT/gno/r/${ADDR}/tollgate"
fi
wait_gpas_passed "$R_PATH"

echo "=== 4. Seed live demo service, credit pass, subscription, and verifiable receipt ==="
printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func RegisterService \
  -args "alpha-oracle" \
  -args "Alpha Price Oracle" \
  -args "https://tollgate-demo.gno.example/api/v1/oracle" \
  -args "$ADDR" \
  -args "1000" -args "10000" -args "5000" -args "100" -args "20" \
  -args "Real-time verifiable market quotes over HTTP 402 (gnolang/x402) and Tollgate credit passes."
sleep 4

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 50000ugnot \
  -pkgpath "$R_PATH" -func OpenPass \
  -args "1" -args "2000" -args "5000"
sleep 4

printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -send 10000ugnot \
  -pkgpath "$R_PATH" -func Subscribe \
  -args "1"
sleep 4

RES_HASH=$(python3 -c "import hashlib; print(hashlib.sha256(b'GET /api/v1/oracle?pair=GNOT-USD').hexdigest())")
printf "%s\n" "$PW" | "$BIN/gnokey" maketx call deployer \
  -home "$KEYHOME" -insecure-password-stdin \
  -remote "$RPC" -chainid "$CHAIN_ID" -broadcast \
  -gas-fee 1000000ugnot -gas-wanted 20000000 \
  -pkgpath "$R_PATH" -func ClaimSettlement \
  -args "1" -args "10" -args "onyx-demo-batch-001" -args "$RES_HASH"
sleep 4

"$BIN/gnokey" query "vm/qrender" -data "${R_PATH}:json/summary" -remote "$RPC"
echo "=== TOLLGATE LIVE ON ONYX-1 ==="
echo "gnoweb: https://onyx.testnets.gno.land/r/${ADDR}/tollgate"
