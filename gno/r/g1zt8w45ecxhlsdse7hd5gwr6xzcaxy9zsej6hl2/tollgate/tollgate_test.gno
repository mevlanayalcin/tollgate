package tollgate

import (
	"chain"
	"chain/banker"
	"strings"
	"testing"

	ptollgate "gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate"
	"gno.land/p/nt/avl/v0"
	"gno.land/p/nt/testutils/v0"
	"gno.land/p/nt/uassert/v0"
	"gno.land/p/nt/urequire/v0"
)

func resetState() {
	services = avl.NewTree()
	slugIndex = avl.NewTree()
	passes = avl.NewTree()
	subs = avl.NewTree()
	settlements = avl.NewTree()
	receipts = avl.NewTree()
	receiptHashIndex = avl.NewTree()
	nonceUsed = avl.NewTree()
	nextServiceID = 0
	nextPassID = 0
	nextSubID = 0
	nextSettlementID = 0
	nextReceiptID = 0
	activePassesCount = 0
	totalDepositedUgnot = 0
	totalSettledUgnot = 0
	totalRefundedUgnot = 0
	totalX402AnchoredUgnot = 0
	totalSubRevenueUgnot = 0
}

func TestEndToEndMeteredPassAndSettlement(cur realm, t *testing.T) {
	defer resetState()

	pkgAddr := chain.PackageAddress("gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate")
	provider := testutils.TestAddress("provider")
	payTo := testutils.TestAddress("payto")
	buyer := testutils.TestAddress("buyer")

	testing.IssueCoins(pkgAddr, chain.NewCoins(chain.NewCoin("ugnot", 1_000_000)))
	testing.SetHeight(100)

	testing.SetRealm(testing.NewUserRealm(provider))
	sid := RegisterService(
		cross(cur), "alpha-oracle", "Alpha Price Oracle",
		"https://api.tollgate.example/v1/oracle", payTo,
		1000, 50000, 500, 100, 20,
		"Real-time verified market feeds over HTTP 402.",
	)
	uassert.Equal(t, uint64(1), sid)

	// P0 #5: default maxPerCallUgnot <= 0 MUST default to svc.unitPriceUgnot (1000), NOT deposit (25000)
	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 25000)))
	pid := OpenPass(cross(cur), sid, 0, 100, "")
	p0, _ := GetPass(pid)
	uassert.Equal(t, int64(1000), p0.MaxPerCallUgnot)

	// Provider claims 10 units -> 5% discount -> 9,500 ugnot net
	resHash := ptollgate.HashResource("GET /v1/oracle?pair=GNOT-USD")
	testing.SetRealm(testing.NewUserRealm(provider))
	stID := ClaimSettlement(cross(cur), pid, 10, "batch-001", resHash, "")
	uassert.Equal(t, uint64(1), stID)

	st, ok := GetSettlement(stID)
	urequire.True(t, ok)
	uassert.Equal(t, int64(9500), st.NetUgnot)

	testing.SkipHeights(20)
	FinalizeSettlement(cross(cur), stID)
	uassert.Equal(t, int64(9500), banker.NewReadonlyBanker().GetCoin(payTo, "ugnot"))

	rid, found := VerifyReceiptByCommitment(st.Commitment)
	urequire.True(t, found)
	uassert.Equal(t, st.ReceiptID, rid)

	// P0 #3: Provider CANNOT claim after pass expiry
	testing.SkipHeights(85) // height 205 >= expiry 200
	uassert.AbortsWithMessage(t, cur, "tollgate: pass expired; claims closed", func() {
		ClaimSettlement(cross(cur), pid, 1, "after-expiry", resHash, "")
	})

	// Buyer closes expired pass and receives remaining 15,500 ugnot
	testing.SetRealm(testing.NewUserRealm(buyer))
	refund := ClosePass(cross(cur), pid)
	uassert.Equal(t, int64(15500), refund)
	uassert.Equal(t, int64(0), activePassesCount)
}

func TestDisputeReclaimTimeoutAndReceiptInvalidation(cur realm, t *testing.T) {
	defer resetState()

	pkgAddr := chain.PackageAddress("gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate")
	provider := testutils.TestAddress("prov2")
	buyer := testutils.TestAddress("buyer2")
	testing.IssueCoins(pkgAddr, chain.NewCoins(chain.NewCoin("ugnot", 500_000)))
	testing.SetHeight(200)

	testing.SetRealm(testing.NewUserRealm(provider))
	sid := RegisterService(cross(cur), "llm-router", "LLM Router", "https://llm.example/api", provider, 500, 0, 0, 0, 15, "Metered LLM")

	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 10000)))
	pid := OpenPass(cross(cur), sid, 1000, 50, "")

	testing.SetRealm(testing.NewUserRealm(provider))
	resHash := ptollgate.HashResource("POST /v1/chat")
	stID := ClaimSettlement(cross(cur), pid, 5, "inv-dispute", resHash, "")
	st, _ := GetSettlement(stID)

	// Buyer disputes within challenge window
	testing.SetRealm(testing.NewUserRealm(buyer))
	DisputeSettlement(cross(cur), stID, "HTTP 502 upstream failure")

	// P0 #2: Reclaim too early must abort
	testing.SkipHeights(20) // height 220 < 215 + 15 = 230
	uassert.AbortsWithMessage(t, cur, "tollgate: provider response window still active", func() {
		ReclaimDisputedSettlement(cross(cur), stID)
	})

	// After provider response window (230), buyer reclaims unilaterally — pass is NOT frozen!
	testing.SkipHeights(15) // height 235 >= 230
	ReclaimDisputedSettlement(cross(cur), stID)
	p, _ := GetPass(pid)
	uassert.Equal(t, int64(0), p.PendingUgnot)
	uassert.Equal(t, int64(10000), p.AvailableUgnot)

	// Refunded settlement invalidates receipt verification
	_, valid := VerifyReceiptByCommitment(st.Commitment)
	uassert.False(t, valid)
	uassert.True(t, strings.Contains(Render("receipt/1"), "INVALIDATED / REFUNDED"))

	// Buyer can now close pass cleanly once expired
	testing.SkipHeights(20)
	uassert.Equal(t, int64(10000), ClosePass(cross(cur), pid))
}

func TestEd25519VoucherOverflowBoundsAndGnowebLinks(cur realm, t *testing.T) {
	defer resetState()

	pkgAddr := chain.PackageAddress("gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate")
	provider := testutils.TestAddress("prov3")
	payTo := testutils.TestAddress("payto3")
	buyer := testutils.TestAddress("buyer3")
	testing.IssueCoins(pkgAddr, chain.NewCoins(chain.NewCoin("ugnot", 500_000)))
	testing.SetHeight(300)

	testing.SetRealm(testing.NewUserRealm(provider))
	sid := RegisterService(cross(cur), "weather-x402", "Weather | [Link](https://evil.test)", "https://x402.example/weather", payTo, 250, 5000, 100, 20, 10, "Forecasts")

	// P0 #4: Overflow amountUgnot in RecordX402Receipt rejected
	resHash := ptollgate.HashResource("GET /weather?city=istanbul")
	uassert.AbortsWithMessage(t, cur, "tollgate: units or amountUgnot out of bounds", func() {
		RecordX402Receipt(cross(cur), sid, buyer, 1, maxInt64, "vandal-1", resHash, "0xabcdef1234567890")
	})

	// Ed25519 voucher pass: forged signature rejected on-chain!
	const testPubHex = "0d853fa898a07eb91f618bb3e8b738b0e45be9b3053799a2c42f8204f5fa3505"
	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 5000)))
	pid := OpenPass(cross(cur), sid, 500, 100, testPubHex)

	testing.SetRealm(testing.NewUserRealm(provider))
	uassert.AbortsWithMessage(t, cur, "tollgate: invalid Ed25519 buyer voucher signature", func() {
		ClaimSettlement(cross(cur), pid, 2, "forged-1", resHash, strings.Repeat("00", 64))
	})

	// Subscription quota wraparound rejected
	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 5000)))
	subID := Subscribe(cross(cur), sid, "")
	uassert.False(t, CheckSubscriptionActive(subID, maxInt64))

	// P0 #1: All gnoweb links use full /r/... path (zero broken leading `](:` links!)
	home := Render("")
	uassert.False(t, strings.Contains(home, "](:"))
	uassert.True(t, strings.Contains(home, "](/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate:service/1)"))
	uassert.True(t, strings.Contains(home, "$help&func=OpenPass"))

	// JSON route escapes Markdown link injection cleanly
	js := Render("json/service/1")
	uassert.False(t, strings.Contains(js, "[Link]"))
	uassert.True(t, strings.Contains(js, `\u005bLink\u005d`))
}
