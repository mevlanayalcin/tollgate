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

	// 1. Register service
	testing.SetRealm(testing.NewUserRealm(provider))
	sid := RegisterService(
		cross(cur),
		"alpha-oracle",
		"Alpha Price Oracle",
		"https://api.tollgate.example/v1/oracle",
		payTo,
		1000,
		50000,
		500,
		100,
		20,
		"Real-time verified market feeds over HTTP 402.",
	)
	uassert.Equal(t, uint64(1), sid)

	svc, ok := GetServiceBySlug("alpha-oracle")
	urequire.True(t, ok)
	uassert.Equal(t, int64(1000), svc.UnitPriceUgnot)

	// 2. Buyer opens prepaid credit pass with 25,000 ugnot
	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 25000)))
	pid := OpenPass(cross(cur), sid, 2000, 100)
	uassert.Equal(t, uint64(1), pid)
	uassert.True(t, CheckPassQuota(pid, 10))
	uassert.False(t, CheckPassQuota(pid, 50))

	// 3. Provider claims 10 units -> 5% volume discount -> 9,500 ugnot net
	resHash := ptollgate.HashResource("GET /v1/oracle?pair=GNOT-USD")
	testing.SetRealm(testing.NewUserRealm(provider))
	stID := ClaimSettlement(cross(cur), pid, 10, "batch-001", resHash)
	uassert.Equal(t, uint64(1), stID)

	st, ok := GetSettlement(stID)
	urequire.True(t, ok)
	uassert.Equal(t, int64(10000), st.GrossUgnot)
	uassert.Equal(t, int64(500), st.DiscountBps)
	uassert.Equal(t, int64(9500), st.NetUgnot)
	uassert.Equal(t, StatusPending, st.Status)

	// Replay nonce rejected
	uassert.AbortsWithMessage(t, cur, "tollgate: duplicate nonce", func() {
		ClaimSettlement(cross(cur), pid, 1, "batch-001", resHash)
	})

	// Finalizing before challenge window ends must fail
	uassert.AbortsWithMessage(t, cur, "tollgate: challenge window still active", func() {
		FinalizeSettlement(cross(cur), stID)
	})

	// Advance past challenge window & finalize
	testing.SkipHeights(20)
	FinalizeSettlement(cross(cur), stID)

	stAfter, _ := GetSettlement(stID)
	uassert.Equal(t, StatusFinalized, stAfter.Status)
	uassert.Equal(t, int64(9500), banker.NewReadonlyBanker().GetCoin(payTo, "ugnot"))

	// Commitment verified on-chain
	rid, found := VerifyReceiptByCommitment(stAfter.Commitment)
	urequire.True(t, found)
	uassert.Equal(t, stAfter.ReceiptID, rid)

	// 4. Buyer requests early close & withdraws remaining 15,500 ugnot
	testing.SetRealm(testing.NewUserRealm(buyer))
	buyerBefore := banker.NewReadonlyBanker().GetCoin(buyer, "ugnot")
	newExpiry := RequestEarlyClose(cross(cur), pid)
	uassert.True(t, newExpiry <= 140)
	testing.SkipHeights(20)
	refund := ClosePass(cross(cur), pid)
	uassert.Equal(t, int64(15500), refund)
	uassert.Equal(t, buyerBefore+15500, banker.NewReadonlyBanker().GetCoin(buyer, "ugnot"))
}

func TestDisputeAndRefundProtection(cur realm, t *testing.T) {
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
	pid := OpenPass(cross(cur), sid, 1000, 50)

	testing.SetRealm(testing.NewUserRealm(provider))
	resHash := ptollgate.HashResource("POST /v1/chat")
	stID := ClaimSettlement(cross(cur), pid, 5, "inv-dispute", resHash)

	testing.SetRealm(testing.NewUserRealm(buyer))
	DisputeSettlement(cross(cur), stID, "HTTP 502 upstream failure; resource not delivered")
	st, _ := GetSettlement(stID)
	uassert.Equal(t, StatusDisputed, st.Status)

	testing.SkipHeights(30)
	testing.SetRealm(testing.NewUserRealm(provider))
	uassert.AbortsWithMessage(t, cur, "tollgate: settlement is not pending", func() {
		FinalizeSettlement(cross(cur), stID)
	})

	RefundDisputedSettlement(cross(cur), stID)
	p, _ := GetPass(pid)
	uassert.Equal(t, int64(0), p.PendingUgnot)
	uassert.Equal(t, int64(10000), p.AvailableUgnot)
}

func TestSubscriptionAndX402ReceiptAndSecurityGuards(cur realm, t *testing.T) {
	defer resetState()

	pkgAddr := chain.PackageAddress("gno.land/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate")
	provider := testutils.TestAddress("prov3")
	payTo := testutils.TestAddress("payto3")
	buyer := testutils.TestAddress("buyer3")
	attacker := testutils.TestAddress("attacker")
	testing.IssueCoins(pkgAddr, chain.NewCoins(chain.NewCoin("ugnot", 500_000)))
	testing.SetHeight(300)

	testing.SetRealm(testing.NewUserRealm(provider))
	sid := RegisterService(cross(cur), "weather-x402", "Weather | Evil #Heading", "https://x402.example/weather", payTo, 250, 5000, 100, 20, 10, "Forecasts")

	testing.SetRealm(testing.NewCodeRealm("gno.land/r/evil/proxy"))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 5000)))
	uassert.AbortsWithMessage(t, cur, "tollgate: payment must be a direct user call", func() {
		Subscribe(cross(cur), sid)
	})

	testing.SetRealm(testing.NewUserRealm(buyer))
	testing.SetOriginSend(chain.NewCoins(chain.NewCoin("ugnot", 7000)))
	buyerBefore := banker.NewReadonlyBanker().GetCoin(buyer, "ugnot")
	subID := Subscribe(cross(cur), sid)
	uassert.Equal(t, uint64(1), subID)
	uassert.True(t, CheckSubscriptionActive(subID, 5))
	uassert.Equal(t, int64(5000), banker.NewReadonlyBanker().GetCoin(payTo, "ugnot"))
	uassert.Equal(t, buyerBefore+2000, banker.NewReadonlyBanker().GetCoin(buyer, "ugnot"))

	testing.SetRealm(testing.NewUserRealm(provider))
	resHash := ptollgate.HashResource("GET /weather?city=istanbul")
	subRid := ConsumeSubscription(cross(cur), subID, 4, "sub-call-1", resHash)
	uassert.Equal(t, uint64(1), subRid)

	x402Rid := RecordX402Receipt(cross(cur), sid, buyer, 1, 250000, "x402-inv-9", resHash, "0xabcdef1234567890")
	uassert.Equal(t, uint64(2), x402Rid)

	testing.SetRealm(testing.NewUserRealm(attacker))
	uassert.AbortsWithMessage(t, cur, "tollgate: only service owner may anchor x402 receipts", func() {
		RecordX402Receipt(cross(cur), sid, buyer, 1, 250000, "x402-fake", resHash, "0xdeadbeef1234")
	})

	home := Render("")
	uassert.True(t, strings.Contains(home, "Tollgate"))
	uassert.True(t, strings.Contains(home, `\|`))
	js := Render("json/summary")
	uassert.True(t, strings.Contains(js, `"protocol":"tollgate-v1"`))
	uassert.True(t, strings.Contains(js, `"totalX402AnchoredUgnot":250000`))
}
