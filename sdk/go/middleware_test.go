package tollgate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeChain struct {
	pass PassInfo
	sub  SubscriptionInfo
}

func (f *fakeChain) Summary(context.Context) (ChainSummary, error) {
	return ChainSummary{ChainID: "onyx-1", Height: 100}, nil
}
func (f *fakeChain) Services(context.Context) ([]ServiceInfo, error) { return nil, nil }
func (f *fakeChain) Service(context.Context, string) (ServiceInfo, error) {
	return ServiceInfo{ID: 1, UnitPriceUgnot: 1000}, nil
}
func (f *fakeChain) Pass(context.Context, uint64) (PassInfo, error) { return f.pass, nil }
func (f *fakeChain) Subscription(context.Context, uint64) (SubscriptionInfo, error) {
	return f.sub, nil
}
func (f *fakeChain) Receipt(context.Context, string) (ReceiptInfo, error) {
	return ReceiptInfo{}, nil
}
func (f *fakeChain) CheckPassQuota(context.Context, uint64, int64) (bool, error) {
	return true, nil
}
func (f *fakeChain) CheckSubscriptionActive(context.Context, uint64, int64) (bool, error) {
	return true, nil
}

type fakeFac struct{}

func (f *fakeFac) Verify(context.Context, string, PaymentRequirements) (VerifyResponse, error) {
	return VerifyResponse{IsValid: true, Payer: "g1payer000000000000000000000000000000000"}, nil
}
func (f *fakeFac) Settle(context.Context, string, PaymentRequirements) (SettleResponse, error) {
	return SettleResponse{Success: true, Transaction: "0xdeadbeef12345678", Payer: "g1payer000000000000000000000000000000000"}, nil
}

type fakeSubmitter struct {
	submitted []PendingReceipt
}

func (s *fakeSubmitter) SubmitReceipt(_ context.Context, r PendingReceipt) (string, error) {
	s.submitted = append(s.submitted, r)
	return "tx-" + r.Nonce, nil
}

func TestMiddlewareAllThreeRailsWithPayWordAndStockX402(t *testing.T) {
	seed := HashResource("tollgate-middleware-test-seed")
	pwChain, err := BuildPayWordChain("onyx-1", seed, 100)
	if err != nil {
		t.Fatalf("build payword chain: %v", err)
	}
	anchorHex := pwChain.AnchorHex()

	fc := &fakeChain{
		pass: PassInfo{
			ID:              7,
			ServiceID:       1,
			Buyer:           "g1buyer000000000000000000000000000000000",
			AvailableUgnot:  50000,
			MaxPerCallUgnot: 2000,
			ExpiryHeight:    500,
			CurrentHeight:   100,
			VoucherPubKey:   anchorHex,
		},
		sub: SubscriptionInfo{
			ID:             3,
			ServiceID:      1,
			Buyer:          "g1buyer000000000000000000000000000000000",
			Active:         true,
			QuotaTotal:     100,
			QuotaRemaining: 80,
			ExpiryHeight:   500,
			CurrentHeight:  100,
			VoucherPubKey:  anchorHex,
		},
	}
	subm := &fakeSubmitter{}
	mw := NewMiddleware(MiddlewareConfig{
		ChainID:     "onyx-1",
		RealmPath:   "gno.land/r/test/tollgate/v2",
		Chain:       fc,
		Facilitator: &fakeFac{},
		Submitter:   subm,
		RequireAuth: true,
		Routes: map[string]RouteSpec{
			"GET /v1/oracle": {
				ServiceID:      1,
				Slug:           "oracle",
				Description:    "Oracle quote",
				PayTo:          "g1prov0000000000000000000000000000000000",
				UnitPriceUgnot: 1000,
				MimeType:       "application/json",
			},
		},
	})

	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"price":1.42}`))
	}))

	// 1. Unpaid probe returns 402 + x402 PAYMENT-REQUIRED
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil))
	if rec1.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", rec1.Code)
	}
	pr, err := DecodePaymentRequired(rec1.Header().Get(HeaderPaymentRequired))
	if err != nil {
		t.Fatalf("decode PAYMENT-REQUIRED: %v", err)
	}

	// 2a. Unauthorized pass call (no PayWord preimage) MUST be rejected with 401!
	reqBad := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	reqBad.Header.Set(HeaderTollgatePass, "7")
	reqBad.Header.Set(HeaderTollgateUnits, "10")
	reqBad.Header.Set(HeaderTollgateNonce, "pass-call-1")
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pass call expected 401, got %d", recBad.Code)
	}

	// 2b. Valid PayWord pass call #1 (10 units -> w_90) succeeds
	proof10, _, err := pwChain.ProofForAnchor(anchorHex, 10)
	if err != nil {
		t.Fatalf("proof10: %v", err)
	}
	reqBad.Header.Set(HeaderTollgateVoucherSig, proof10)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, reqBad)
	if rec2.Code != http.StatusOK || rec2.Header().Get(HeaderTollgateNetUgnot) != "9500" {
		t.Fatalf("PayWord pass call #1 failed: %d %s", rec2.Code, rec2.Body.String())
	}

	// Duplicate nonce returns 409 Conflict
	recDup := httptest.NewRecorder()
	handler.ServeHTTP(recDup, reqBad)
	if recDup.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate nonce, got %d", recDup.Code)
	}

	// 2c. Sequential PayWord pass call #2 (5 units from w_90 -> w_85) proves chained off-chain auth!
	proofNext5, _, err := pwChain.ProofForAnchor(proof10, 5)
	if err != nil {
		t.Fatalf("proofNext5: %v", err)
	}
	req2b := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req2b.Header.Set(HeaderTollgatePass, "7")
	req2b.Header.Set(HeaderTollgateUnits, "5")
	req2b.Header.Set(HeaderTollgateNonce, "pass-call-2")
	req2b.Header.Set(HeaderTollgateVoucherSig, proofNext5)
	rec2b := httptest.NewRecorder()
	handler.ServeHTTP(rec2b, req2b)
	if rec2b.Code != http.StatusOK || rec2b.Header().Get(HeaderTollgateNetUgnot) != "5000" {
		t.Fatalf("PayWord chained pass call #2 failed: %d %s", rec2b.Code, rec2b.Body.String())
	}

	// 3. Authenticated subscription fast-path (1 unit -> w_99)
	proofSub1, _, err := pwChain.ProofForAnchor(anchorHex, 1)
	if err != nil {
		t.Fatalf("proofSub1: %v", err)
	}
	req3 := httptest.NewRequest(http.MethodGet, "/v1/oracle?pair=GNOT-USD", nil)
	req3.Header.Set(HeaderTollgateSub, "3")
	req3.Header.Set(HeaderTollgateNonce, "sub-call-1")
	req3.Header.Set(HeaderTollgateVoucherSig, proofSub1)
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
	rec4Dup := httptest.NewRecorder()
	handler.ServeHTTP(rec4Dup, req4)
	if rec4Dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 on x402 replay, got %d", rec4Dup.Code)
	}

	// 5. Real FlushSettlements submits all 4 receipts and reconciles debits
	n, err := mw.FlushSettlements(context.Background())
	if err != nil || n != 4 || len(subm.submitted) != 4 {
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
		RealmPath: "gno.land/r/test/tollgate/v2",
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
	settled2, err2 := mw.FlushSettlements(context.Background())
	if err2 != nil || settled2 != 0 || sub.calls != 2 {
		t.Fatalf("expected dead-lettered receipt skipped on retry: settled2=%d err2=%v calls=%d", settled2, err2, sub.calls)
	}
}

// Fable 5.2 PayWord concurrency & failure regression tests:

func TestPayWordConcurrentRequestsPreserveChainOrder(t *testing.T) {
	seed := HashResource("tollgate-concurrent-seed")
	pw, _ := BuildPayWordChain("onyx-1", seed, 50)
	anchor := pw.AnchorHex()
	proof1, _, _ := pw.ProofForAnchor(anchor, 2)
	proof2, _, _ := pw.ProofForAnchor(proof1, 3)

	fc := &fakeChain{
		pass: PassInfo{
			ID: 1, ServiceID: 1, Buyer: "g1buyer000000000000000000000000000000000",
			AvailableUgnot: 50000, MaxPerCallUgnot: 2000, ExpiryHeight: 500, CurrentHeight: 100,
			VoucherPubKey: anchor,
		},
	}
	subm := &fakeSubmitter{}
	mw := NewMiddleware(MiddlewareConfig{
		ChainID: "onyx-1", RealmPath: "gno.land/r/test/tollgate/v2",
		Chain: fc, Submitter: subm, RequireAuth: true,
		Routes: map[string]RouteSpec{
			"GET /v1/slow": {ServiceID: 1, Slug: "s", PayTo: "g1prov0000000000000000000000000000000000", UnitPriceUgnot: 1000},
		},
	})

	started1 := make(chan struct{})
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderTollgateNonce) == "c-1" {
			close(started1)
			// Slow down request #1 so an unlocked implementation would finish #2 first
			select {
			case <-r.Context().Done():
			case <-make(chan struct{}):
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = h
	// Deterministic version with short sleep so request #1 runs slower than #2
	entered1 := make(chan struct{})
	h2 := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderTollgateNonce) == "c-1" {
			close(entered1)
			// Sleep briefly while #2 waits on the per-pass mutex
			ctx, cancel := context.WithTimeout(r.Context(), 40_000_000) // 40ms
			defer cancel()
			<-ctx.Done()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	done1 := make(chan int, 1)
	done2 := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/v1/slow", nil)
		req.Header.Set(HeaderTollgatePass, "1")
		req.Header.Set(HeaderTollgateUnits, "2")
		req.Header.Set(HeaderTollgateNonce, "c-1")
		req.Header.Set(HeaderTollgateVoucherSig, proof1)
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		done1 <- rec.Code
	}()
	<-entered1
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/v1/slow", nil)
		req.Header.Set(HeaderTollgatePass, "1")
		req.Header.Set(HeaderTollgateUnits, "3")
		req.Header.Set(HeaderTollgateNonce, "c-2")
		req.Header.Set(HeaderTollgateVoucherSig, proof2)
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		done2 <- rec.Code
	}()

	if c1, c2 := <-done1, <-done2; c1 != 200 || c2 != 200 {
		t.Fatalf("expected both concurrent calls 200, got c1=%d c2=%d", c1, c2)
	}
	n, err := mw.FlushSettlements(context.Background())
	if err != nil || n != 2 || len(subm.submitted) != 2 {
		t.Fatalf("flush failed: n=%d err=%v", n, err)
	}
	if subm.submitted[0].Nonce != "c-1" || subm.submitted[1].Nonce != "c-2" {
		t.Fatalf("expected chain order c-1 then c-2, got %s then %s", subm.submitted[0].Nonce, subm.submitted[1].Nonce)
	}
}

func TestPayWord5xxRollbackRestoresAnchorForRetry(t *testing.T) {
	seed := HashResource("tollgate-rollback-seed")
	pw, _ := BuildPayWordChain("onyx-1", seed, 50)
	anchor := pw.AnchorHex()
	proof1, _, _ := pw.ProofForAnchor(anchor, 2)

	fc := &fakeChain{
		pass: PassInfo{
			ID: 1, ServiceID: 1, Buyer: "g1buyer000000000000000000000000000000000",
			AvailableUgnot: 50000, MaxPerCallUgnot: 2000, ExpiryHeight: 500, CurrentHeight: 100,
			VoucherPubKey: anchor,
		},
	}
	mw := NewMiddleware(MiddlewareConfig{
		ChainID: "onyx-1", RealmPath: "gno.land/r/test/tollgate/v2",
		Chain: fc, RequireAuth: true,
		Routes: map[string]RouteSpec{
			"GET /v1/flaky": {ServiceID: 1, Slug: "f", PayTo: "g1prov0000000000000000000000000000000000", UnitPriceUgnot: 1000},
		},
	})
	failFirst := true
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failFirst {
			failFirst = false
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "/v1/flaky", nil)
	req1.Header.Set(HeaderTollgatePass, "1")
	req1.Header.Set(HeaderTollgateUnits, "2")
	req1.Header.Set(HeaderTollgateNonce, "rb-1")
	req1.Header.Set(HeaderTollgateVoucherSig, proof1)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != 500 {
		t.Fatalf("expected 500, got %d", rec1.Code)
	}

	// Retry with same proof1 MUST succeed because 500 rolled back anchor & nonce
	req2 := httptest.NewRequest(http.MethodGet, "/v1/flaky", nil)
	req2.Header.Set(HeaderTollgatePass, "1")
	req2.Header.Set(HeaderTollgateUnits, "2")
	req2.Header.Set(HeaderTollgateNonce, "rb-1")
	req2.Header.Set(HeaderTollgateVoucherSig, proof1)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("expected retry 200 after rollback, got %d (%s)", rec2.Code, rec2.Body.String())
	}
}

type flakyTransientSubmitter struct {
	failTimes int
	calls     int
	settled   []string
}

func (f *flakyTransientSubmitter) SubmitReceipt(_ context.Context, r PendingReceipt) (string, error) {
	f.calls++
	if f.calls <= f.failTimes {
		return "", errors.New("rpc dial timeout")
	}
	f.settled = append(f.settled, r.Nonce)
	return "tx-" + r.Nonce, nil
}

func TestPayWordTransientRPCOutageDoesNotDeadLetterChain(t *testing.T) {
	sub := &flakyTransientSubmitter{failTimes: 4}
	mw := NewMiddleware(MiddlewareConfig{
		ChainID: "onyx-1", RealmPath: "gno.land/r/test/tollgate/v2",
		Submitter: sub,
	})
	mw.mu.Lock()
	mw.receipts = []PendingReceipt{
		{Rail: "metered-pass", PassID: 1, Units: 2, AmountUgnot: 2000, Nonce: "t-1", BuyerSigHex: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Rail: "metered-pass", PassID: 1, Units: 3, AmountUgnot: 3000, Nonce: "t-2", BuyerSigHex: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	mw.mu.Unlock()

	// 4 consecutive transient outages (>3 attempts!) must NOT dead-letter PayWord receipts or settle t-2 out of order!
	for i := 0; i < 4; i++ {
		n, err := mw.FlushSettlements(context.Background())
		if err == nil || n != 0 {
			t.Fatalf("tick %d: expected transient err and 0 settled, got n=%d err=%v", i, n, err)
		}
		recs := mw.Receipts()
		if recs[0].DeadLetter || recs[1].DeadLetter || len(sub.settled) != 0 {
			t.Fatalf("tick %d: PayWord chain must never dead-letter on transient RPC error: %+v", i, recs)
		}
	}

	// On recovery, BOTH t-1 and t-2 settle cleanly in exact order!
	n, err := mw.FlushSettlements(context.Background())
	if err != nil || n != 2 || len(sub.settled) != 2 || sub.settled[0] != "t-1" || sub.settled[1] != "t-2" {
		t.Fatalf("recovery flush failed: n=%d err=%v settled=%v", n, err, sub.settled)
	}
}
