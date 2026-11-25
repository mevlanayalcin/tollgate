package tollgate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeChain struct {
	pass PassInfo
	sub  SubscriptionInfo
}

func (f *fakeChain) Summary(context.Context) (ChainSummary, error)            { return ChainSummary{}, nil }
func (f *fakeChain) Services(context.Context) ([]ServiceInfo, error)          { return nil, nil }
func (f *fakeChain) Service(context.Context, string) (ServiceInfo, error)     { return ServiceInfo{}, nil }
func (f *fakeChain) Receipt(context.Context, string) (ReceiptInfo, error)     { return ReceiptInfo{}, nil }
func (f *fakeChain) Pass(context.Context, uint64) (PassInfo, error)           { return f.pass, nil }
func (f *fakeChain) Subscription(context.Context, uint64) (SubscriptionInfo, error) {
	return f.sub, nil
}
func (f *fakeChain) CheckPassQuota(context.Context, uint64, int64) (bool, error) { return true, nil }
func (f *fakeChain) CheckSubscriptionActive(context.Context, uint64, int64) (bool, error) {
	return f.sub.Active, nil
}

type fakeFacilitator struct{}

func (f *fakeFacilitator) Verify(context.Context, string, PaymentRequirements) (VerifyResponse, error) {
	return VerifyResponse{IsValid: true, Payer: "g1x402buyer00000000000000000000000000000"}, nil
}
func (f *fakeFacilitator) Settle(context.Context, string, PaymentRequirements) (SettleResponse, error) {
	return SettleResponse{
		Success:     true,
		Transaction: "0x1234567890abcdef",
		Network:     "gno:onyx-1",
		Payer:       "g1x402buyer00000000000000000000000000000",
	}, nil
}

func TestMiddlewareAllThreeRails(t *testing.T) {
	chain := &fakeChain{
		pass: PassInfo{
			ID: 7, ServiceID: 1, Buyer: "g1passbuyer0000000000000000000000000000",
			DepositUgnot: 25000, AvailableUgnot: 25000, MaxPerCallUgnot: 2000,
			ExpiryHeight: 500, CurrentHeight: 100,
		},
		sub: SubscriptionInfo{
			ID: 3, ServiceID: 1, Buyer: "g1subbuyer00000000000000000000000000000",
			QuotaTotal: 50, QuotaRemaining: 50, Active: true,
		},
	}

	mw := NewMiddleware(MiddlewareConfig{
		ChainID:     "onyx-1",
		RealmPath:   "gno.land/r/test/tollgate",
		Chain:       chain,
		Facilitator: &fakeFacilitator{},
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
	if pr.X402Version != 2 || len(pr.Accepts) != 1 || pr.Accepts[0].Network != "gno:onyx-1" {
		t.Fatalf("unexpected PaymentRequired: %+v", pr)
	}

	// 2. Prepaid Pass fast-path (10 units -> 5% volume discount -> 9500 ugnot)
	req2 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req2.Header.Set(HeaderTollgatePass, "7")
	req2.Header.Set(HeaderTollgateUnits, "10")
	req2.Header.Set(HeaderTollgateNonce, "pass-call-1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("pass call expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get(HeaderTollgateRail) != "metered-pass" || rec2.Header().Get(HeaderTollgateNetUgnot) != "9500" {
		t.Fatalf("unexpected pass headers: rail=%s net=%s", rec2.Header().Get(HeaderTollgateRail), rec2.Header().Get(HeaderTollgateNetUgnot))
	}
	commit := rec2.Header().Get(HeaderTollgateCommitment)
	if !VerifyReceiptCommitment(
		commit, "onyx-1", 1, 7, chain.pass.Buyer,
		"g1prov0000000000000000000000000000000000", 10, 9500, "pass-call-1",
		HashResource("GET /v1/oracle?pair=GNOT-USD"),
	) {
		t.Fatal("pass receipt commitment verification failed")
	}

	// Duplicate nonce must return 409 Conflict
	recDup := httptest.NewRecorder()
	handler.ServeHTTP(recDup, req2)
	if recDup.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate nonce, got %d", recDup.Code)
	}

	// 3. Subscription fast-path
	req3 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req3.Header.Set(HeaderTollgateSub, "3")
	req3.Header.Set(HeaderTollgateNonce, "sub-call-1")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || rec3.Header().Get(HeaderTollgateRail) != "subscription" {
		t.Fatalf("sub call failed: %d", rec3.Code)
	}

	// 4. Native x402 direct payment rail
	payloadJSON, _ := json.Marshal(map[string]any{"x402Version": 2, "payload": map[string]any{"transaction": "AAAA"}})
	req4 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req4.Header.Set(HeaderPaymentSignature, base64.StdEncoding.EncodeToString(payloadJSON))
	req4.Header.Set(HeaderTollgateNonce, "x402-call-1")
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK || rec4.Header().Get(HeaderTollgateRail) != "x402-direct" {
		t.Fatalf("x402 call failed: %d %s", rec4.Code, rec4.Body.String())
	}
	if rec4.Header().Get(HeaderPaymentResponse) == "" {
		t.Fatal("missing PAYMENT-RESPONSE header")
	}

	if len(mw.Receipts()) != 3 {
		t.Fatalf("expected 3 recorded receipts, got %d", len(mw.Receipts()))
	}
}
