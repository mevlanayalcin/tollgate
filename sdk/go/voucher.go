package tollgate

import (
	"crypto/ed25519"
	"encoding/hex"
)

func SignVoucher(
	priv ed25519.PrivateKey,
	chainID string,
	serviceID, passID, subID uint64,
	units int64,
	nonce, resourceHash string,
) string {
	msg := VoucherMessage(chainID, serviceID, passID, subID, units, nonce, resourceHash)
	return hex.EncodeToString(ed25519.Sign(priv, []byte(msg)))
}

func VerifyVoucher(
	pubKeyHex, sigHex string,
	chainID string,
	serviceID, passID, subID uint64,
	units int64,
	nonce, resourceHash string,
) bool {
	if !IsValidHexDigest(pubKeyHex) || len(sigHex) != 128 {
		return false
	}
	pub, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := VoucherMessage(chainID, serviceID, passID, subID, units, nonce, resourceHash)
	return ed25519.Verify(pub, []byte(msg), sig)
}
