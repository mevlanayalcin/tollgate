// Package tollgate implements the off-chain Go SDK, Gno RPC client, x402 v2
// payment bridge, and dual-rail HTTP 402 middleware for the Tollgate protocol.
package tollgate

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
)

const (
	ProtocolVersion = "tollgate-v1"
	ReceiptDomain   = "tollgate-receipt-v1"
	MemoPrefix      = "tollgate:v1:"
	MaxMemoBytes    = 256
)

var (
	ErrInvalidPrice   = errors.New("tollgate: unit price must be positive")
	ErrInvalidUnits   = errors.New("tollgate: units must be positive")
	ErrOverflow       = errors.New("tollgate: arithmetic overflow")
	ErrInvalidBalance = errors.New("tollgate: invalid balance accounting")
	ErrInvalidMemo    = errors.New("tollgate: invalid invoice memo or nonce")
)

type ChargeBreakdown struct {
	Units       int64 `json:"units"`
	UnitPrice   int64 `json:"unitPrice"`
	GrossUgnot  int64 `json:"grossUgnot"`
	DiscountBps int64 `json:"discountBps"`
	Discount    int64 `json:"discount"`
	NetUgnot    int64 `json:"netUgnot"`
}

func TierDiscountBps(units int64) int64 {
	switch {
	case units >= 1000:
		return 2000
	case units >= 250:
		return 1500
	case units >= 50:
		return 1000
	case units >= 10:
		return 500
	default:
		return 0
	}
}

func ComputeCharge(unitPrice, units int64) (ChargeBreakdown, error) {
	if unitPrice <= 0 {
		return ChargeBreakdown{}, ErrInvalidPrice
	}
	if units <= 0 {
		return ChargeBreakdown{}, ErrInvalidUnits
	}
	if units > math.MaxInt64/unitPrice {
		return ChargeBreakdown{}, ErrOverflow
	}
	gross := unitPrice * units
	bps := TierDiscountBps(units)
	var discount int64
	if bps > 0 {
		if gross > math.MaxInt64/bps {
			discount = (gross / 10000) * bps
		} else {
			discount = (gross * bps) / 10000
		}
	}
	net := gross - discount
	if net < 1 {
		net = 1
		discount = gross - net
	}
	return ChargeBreakdown{
		Units:       units,
		UnitPrice:   unitPrice,
		GrossUgnot:  gross,
		DiscountBps: bps,
		Discount:    discount,
		NetUgnot:    net,
	}, nil
}

func ComputeAvailable(deposit, spent, pending int64) (int64, error) {
	if deposit < 0 || spent < 0 || pending < 0 {
		return 0, ErrInvalidBalance
	}
	if spent > deposit || pending > deposit-spent {
		return 0, ErrInvalidBalance
	}
	return deposit - spent - pending, nil
}

func ValidateNonce(nonce string) bool {
	n := len(nonce)
	if n < 1 || n > 96 {
		return false
	}
	for i := 0; i < n; i++ {
		c := nonce[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func IsValidHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func FormatInvoiceMemo(serviceID uint64, nonce string) (string, error) {
	if serviceID == 0 || !ValidateNonce(nonce) {
		return "", ErrInvalidMemo
	}
	memo := MemoPrefix + strconv.FormatUint(serviceID, 10) + ":" + nonce
	if len(memo) > MaxMemoBytes {
		return "", ErrInvalidMemo
	}
	return memo, nil
}

func ParseInvoiceMemo(memo string) (uint64, string, bool) {
	if len(memo) > MaxMemoBytes || !strings.HasPrefix(memo, MemoPrefix) {
		return 0, "", false
	}
	rest := memo[len(MemoPrefix):]
	sep := strings.IndexByte(rest, ':')
	if sep <= 0 || sep >= len(rest)-1 {
		return 0, "", false
	}
	id, err := strconv.ParseUint(rest[:sep], 10, 64)
	if err != nil || id == 0 {
		return 0, "", false
	}
	n := rest[sep+1:]
	if !ValidateNonce(n) {
		return 0, "", false
	}
	return id, n, true
}

func appendLP(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
	b.WriteByte('|')
}

// ReceiptCommitment matches gno.land/p/<deployer>/tollgate.ReceiptCommitment byte-for-byte.
func ReceiptCommitment(
	chainID string,
	serviceID uint64,
	passID uint64,
	buyer string,
	provider string,
	units int64,
	amountUgnot int64,
	nonce string,
	resourceHash string,
) string {
	var b strings.Builder
	b.WriteString(ReceiptDomain)
	b.WriteByte('|')
	appendLP(&b, chainID)
	b.WriteString(strconv.FormatUint(serviceID, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatUint(passID, 10))
	b.WriteByte('|')
	appendLP(&b, buyer)
	appendLP(&b, provider)
	b.WriteString(strconv.FormatInt(units, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(amountUgnot, 10))
	b.WriteByte('|')
	appendLP(&b, nonce)
	appendLP(&b, resourceHash)

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func VerifyReceiptCommitment(
	expectedHex string,
	chainID string,
	serviceID uint64,
	passID uint64,
	buyer string,
	provider string,
	units int64,
	amountUgnot int64,
	nonce string,
	resourceHash string,
) bool {
	if !IsValidHexDigest(expectedHex) {
		return false
	}
	got := ReceiptCommitment(
		chainID, serviceID, passID, buyer, provider,
		units, amountUgnot, nonce, resourceHash,
	)
	return subtle.ConstantTimeCompare([]byte(got), []byte(expectedHex)) == 1
}

func HashResource(descriptor string) string {
	sum := sha256.Sum256([]byte(descriptor))
	return hex.EncodeToString(sum[:])
}
