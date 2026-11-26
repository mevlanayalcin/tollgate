package tollgate

import (
	"context"
	"errors"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type fakeChain struct {
	pass PassInfo
	sub  SubscriptionInfo
}

func (f *fakeChain) Summary(context.Context) (ChainSummary, error)                  { return ChainSummary{}, nil }
func (f *fakeChain) Services(context.Context) ([]ServiceInfo, error)                { return nil, nil }
func (f *fakeChain) Service(context.Context, string) (ServiceInfo, error)           { return ServiceInfo{}, nil }
func (f *fakeChain) Receipt(context.Context, string) (ReceiptInfo, error)           { return ReceiptInfo{}, nil }
func (f *fakeChain) Pass(context.Context, uint64) (PassInfo, error)                 { return f.pass, nil }
func (f *fakeChain) Subscription(context.Context, uint64) (SubscriptionInfo, error) { return f.sub, nil }
func (f *fakeChain) CheckPassQuota(context.Context, uint64, int64) (bool, error)    { return true, nil }
func (f *fakeChain) CheckSubscriptionActive(context.Context, uint64, int64) (bool, error) {
	return f.sub.Active, nil
}

type strictMemoFacilitator struct {
	lastVerifiedMemo string
}

func (f *strictMemoFacilitator) Verify(_ context.Context, paymentHeader string, req PaymentRequirements) (VerifyResponse, error) {
	memo, _ := ExtractX402AcceptedMemo(paymentHeader)
	if req.Extra["memo"] != memo {
		return VerifyResponse{IsValid: false, InvalidReason: "invalid_exact_gno_payload_memo_mismatch"}, nil
	}
	f.lastVerifiedMemo = memo
	return VerifyResponse{IsValid: true, Payer: "g1x402buyer00000000000000000000000000000"}, nil
}
func (f *strictMemoFacilitator) Settle(context.Context, string, PaymentRequirements) (SettleResponse, error) {
	return SettleResponse{
		Success:     true,
		Transaction: "0x1234567890abcdef",
		Network:     "gno:onyx-1",
		Payer:       "g1x402buyer00000000000000000000000000000",
	}, nil
}

type fakeSubmitter struct{ submitted []PendingReceipt }

func (s *fakeSubmitter) SubmitReceipt(_ context.Context, r PendingReceipt) (string, error) {
	s.submitted = append(s.submitted, r)
	return "txhash-" + r.Nonce, nil
}

func TestMiddlewareAllThreeRailsWithEd25519AndStockX402(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)

	chain := &fakeChain{
		pass: PassInfo{
			ID: 7, ServiceID: 1, Buyer: "g1passbuyer0000000000000000000000000000",
			DepositUgnot: 25000, AvailableUgnot: 25000, MaxPerCallUgnot: 2000,
			ExpiryHeight: 500, CurrentHeight: 100, VoucherPubKey: pubHex,
		},
		sub: SubscriptionInfo{
			ID: 3, ServiceID: 1, Buyer: "g1subbuyer00000000000000000000000000000",
			QuotaTotal: 50, QuotaRemaining: 50, Active: true, VoucherPubKey: pubHex,
		},
	}

	fac := &strictMemoFacilitator{}
	subm := &fakeSubmitter{}
	statePath := filepath.Join(t.TempDir(), "state.json")

	mw := NewMiddleware(MiddlewareConfig{
		ChainID:     "onyx-1",
		RealmPath:   "gno.land/r/test/tollgate",
		Chain:       chain,
		Facilitator: fac,
		Submitter:   subm,
		StateFile:   statePath,
		RequireAuth: true,
		Routes: map[string]RouteSpec{
			"GET /v1/oracle": {
				ServiceID:      1,
				Slug:           "alpha-oracle",
				Description:    "Verified market feed",
				PayTo:          "g1prov0000000000000000000000000000000000",
				UnitPriceUgnot: 1000,
			},
		},
	})

	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pair":"GNOT-USD","price":1.42}`))
	}))

	// 1. Unpaid request -> 402 with valid PAYMENT-REQUIRED header
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil))
	if rec1.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", rec1.Code)
	}
	pr, err := DecodePaymentRequired(rec1.Header().Get(HeaderPaymentRequired))
	if err != nil {
		t.Fatalf("decode PAYMENT-REQUIRED: %v", err)
	}

	// 2a. Unauthorized pass call (no Ed25519 voucher signature) MUST be rejected with 401!
	reqBad := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	reqBad.Header.Set(HeaderTollgatePass, "7")
	reqBad.Header.Set(HeaderTollgateUnits, "10")
	reqBad.Header.Set(HeaderTollgateNonce, "pass-call-1")
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pass call expected 401, got %d", recBad.Code)
	}

	// 2b. Authenticated Ed25519 pass call succeeds
	resHash := HashResource("GET /v1/oracle?pair=GNOT-USD")
	sigPass := SignVoucher(priv, "onyx-1", 1, 7, 0, 10, "pass-call-1", resHash)
	reqBad.Header.Set(HeaderTollgateVoucherSig, sigPass)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, reqBad)
	if rec2.Code != http.StatusOK || rec2.Header().Get(HeaderTollgateNetUgnot) != "9500" {
		t.Fatalf("signed pass call failed: %d %s", rec2.Code, rec2.Body.String())
	}

	// Duplicate nonce returns 409 Conflict
	recDup := httptest.NewRecorder()
	handler.ServeHTTP(recDup, reqBad)
	if recDup.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate nonce, got %d", recDup.Code)
	}

	// 3. Authenticated subscription fast-path
	sigSub := SignVoucher(priv, "onyx-1", 1, 0, 3, 1, "sub-call-1", resHash)
	req3 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req3.Header.Set(HeaderTollgateSub, "3")
	req3.Header.Set(HeaderTollgateNonce, "sub-call-1")
	req3.Header.Set(HeaderTollgateVoucherSig, sigSub)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || rec3.Header().Get(HeaderTollgateRail) != "subscription" {
		t.Fatalf("sub call failed: %d", rec3.Code)
	}

	// 4. Stock x402 retry WITHOUT X-Tollgate-Nonce (carries memo only in accepted.extra.memo)
	payloadJSON, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"accepted":    pr.Accepts[0],
		"payload":     map[string]any{"transaction": "AAAA"},
	})
	req4 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req4.Header.Set(HeaderPaymentSignature, base64.StdEncoding.EncodeToString(payloadJSON))
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK || rec4.Header().Get(HeaderTollgateRail) != "x402-direct" {
		t.Fatalf("stock x402 retry failed: %d %s", rec4.Code, rec4.Body.String())
	}
	// Replaying the same stock x402 memo MUST return 409 Conflict
	rec4Dup := httptest.NewRecorder()
	handler.ServeHTTP(rec4Dup, req4)
	if rec4Dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 on x402 replay, got %d", rec4Dup.Code)
	}

	// 5. Real FlushSettlements submits all 3 receipts and reconciles debits
	n, err := mw.FlushSettlements(context.Background())
	if err != nil || n != 3 || len(subm.submitted) != 3 {
		t.Fatalf("FlushSettlements failed: n=%d err=%v", n, err)
	}
}

type selectiveFailSubmitter struct {
	failNonce string
	calls     int
}

func (s *selectiveFailSubmitter) SubmitReceipt(_ context.Context, r PendingReceipt) (string, error) {
	s.calls++
	if r.Nonce == s.failNonce {
		return "", errors.New("tollgate: pass expired; claims closed")
	}
	return "tx-ok-" + r.Nonce, nil
}

func TestFlushSettlementsDeadLetterDoesNotBlockQueue(t *testing.T) {
	sub := &selectiveFailSubmitter{failNonce: "bad-1"}
	mw := NewMiddleware(MiddlewareConfig{
		ChainID:   "onyx-1",
		RealmPath: "gno.land/r/test/tollgate",
		Submitter: sub,
	})
	mw.mu.Lock()
	mw.receipts = []PendingReceipt{
		{Rail: "metered-pass", PassID: 1, AmountUgnot: 1000, Nonce: "bad-1"},
		{Rail: "metered-pass", PassID: 1, AmountUgnot: 2000, Nonce: "good-2"},
	}
	mw.localDebits[1] = 3000
	mw.mu.Unlock()

	settled, err := mw.FlushSettlements(context.Background())
	if err == nil {
		t.Fatalf("expected error from bad-1, got nil")
	}
	if settled != 1 {
		t.Fatalf("expected good-2 to still settle (1), got %d", settled)
	}
	recs := mw.Receipts()
	if !recs[0].DeadLetter || recs[0].Settled {
		t.Fatalf("expected bad-1 to be dead-lettered, got %+v", recs[0])
	}
	if !recs[1].Settled || recs[1].TxHash != "tx-ok-good-2" {
		t.Fatalf("expected good-2 settled, got %+v", recs[1])
	}
	// Second flush should skip dead-lettered bad-1 cleanly
	settled2, err2 := mw.FlushSettlements(context.Background())
	if err2 != nil || settled2 != 0 || sub.calls != 2 {
		t.Fatalf("expected dead-lettered receipt skipped on retry: settled2=%d err2=%v calls=%d", settled2, err2, sub.calls)
	}
}
