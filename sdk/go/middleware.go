package tollgate

import (
	"bytes"
	"encoding/base64"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type RouteSpec struct {
	ServiceID      uint64
	Slug           string
	Description    string
	PayTo          string
	UnitPriceUgnot int64
	MimeType       string
}

type PendingReceipt struct {
	Rail         string    `json:"rail"`
	ServiceID    uint64    `json:"serviceId"`
	PassID       uint64    `json:"passId"`
	SubID        uint64    `json:"subId"`
	Buyer        string    `json:"buyer"`
	Provider     string    `json:"provider"`
	Units        int64     `json:"units"`
	GrossUgnot   int64     `json:"grossUgnot"`
	DiscountBps  int64     `json:"discountBps"`
	AmountUgnot  int64     `json:"amountUgnot"`
	Nonce        string    `json:"nonce"`
	ResourceHash string    `json:"resourceHash"`
	Commitment   string    `json:"commitment"`
	TxHash       string    `json:"txHash,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MiddlewareConfig struct {
	ChainID     string
	RealmPath   string
	Chain       ChainReader
	Facilitator Facilitator
	Routes      map[string]RouteSpec
}

type Middleware struct {
	cfg           MiddlewareConfig
	mu            sync.Mutex
	usedNonces    map[string]struct{}
	localDebits   map[uint64]int64
	localSubUsage map[uint64]int64
	receipts      []PendingReceipt
}

func NewMiddleware(cfg MiddlewareConfig) *Middleware {
	return &Middleware{
		cfg:           cfg,
		usedNonces:    make(map[string]struct{}),
		localDebits:   make(map[uint64]int64),
		localSubUsage: make(map[uint64]int64),
	}
}

func (m *Middleware) Receipts() []PendingReceipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PendingReceipt, len(m.receipts))
	copy(out, m.receipts)
	return out
}

type bufferedResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), code: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(statusCode int)  { b.code = statusCode }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	for k, vals := range b.header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(b.code)
	_, _ = w.Write(b.body.Bytes())
}

func randomNonce() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "tg-" + hex.EncodeToString(buf[:])
}

// Wrap protects an http.Handler with dual-rail Tollgate + x402 v2 payment enforcement.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeKey := r.Method + " " + r.URL.Path
		spec, ok := m.cfg.Routes[routeKey]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		units := int64(1)
		if rawUnits := r.Header.Get(HeaderTollgateUnits); rawUnits != "" {
			if parsed, err := strconv.ParseInt(rawUnits, 10, 64); err == nil && parsed > 0 && parsed <= 10000 {
				units = parsed
			}
		}

		nonce := r.Header.Get(HeaderTollgateNonce)
		if nonce == "" {
			nonce = randomNonce()
		} else if !ValidateNonce(nonce) {
			http.Error(w, `{"error":"invalid X-Tollgate-Nonce"}`, http.StatusBadRequest)
			return
		}

		resDescriptor := r.Method + " " + r.URL.RequestURI()
		resHash := HashResource(resDescriptor)

		// Rail 1: Subscription fast-path
		if rawSub := r.Header.Get(HeaderTollgateSub); rawSub != "" {
			subID, err := strconv.ParseUint(rawSub, 10, 64)
			if err != nil || subID == 0 {
				m.write402(w, r, spec, units, nonce, "invalid X-Tollgate-Sub")
				return
			}
			m.handleSubscription(w, r, next, spec, subID, units, nonce, resHash)
			return
		}

		// Rail 2: Prepaid Credit Pass fast-path
		if rawPass := r.Header.Get(HeaderTollgatePass); rawPass != "" {
			passID, err := strconv.ParseUint(rawPass, 10, 64)
			if err != nil || passID == 0 {
				m.write402(w, r, spec, units, nonce, "invalid X-Tollgate-Pass")
				return
			}
			m.handlePass(w, r, next, spec, passID, units, nonce, resHash)
			return
		}

		// Rail 3: Stock gnolang/x402 direct payment
		if sig := r.Header.Get(HeaderPaymentSignature); sig != "" {
			m.handleX402(w, r, next, spec, units, nonce, resHash, sig)
			return
		}

		// Unpaid request: emit x402 v2 PAYMENT-REQUIRED + Tollgate rail discovery headers
		m.write402(w, r, spec, units, nonce, "Payment required")
	})
}

func (m *Middleware) reserveNonce(serviceID uint64, nonce string) bool {
	key := strconv.FormatUint(serviceID, 10) + ":" + nonce
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.usedNonces[key]; exists {
		return false
	}
	m.usedNonces[key] = struct{}{}
	return true
}

func (m *Middleware) handleSubscription(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	spec RouteSpec,
	subID uint64,
	units int64,
	nonce string,
	resHash string,
) {
	if m.cfg.Chain == nil {
		m.write402(w, r, spec, units, nonce, "chain reader unavailable")
		return
	}
	sub, err := m.cfg.Chain.Subscription(r.Context(), subID)
	if err != nil || !sub.Active || sub.ServiceID != spec.ServiceID {
		m.write402(w, r, spec, units, nonce, "subscription inactive or wrong service")
		return
	}

	m.mu.Lock()
	localUsed := m.localSubUsage[subID]
	if localUsed+units > sub.QuotaRemaining {
		m.mu.Unlock()
		m.write402(w, r, spec, units, nonce, "subscription quota exhausted")
		return
	}
	m.mu.Unlock()

	if !m.reserveNonce(spec.ServiceID, nonce) {
		http.Error(w, `{"error":"duplicate nonce"}`, http.StatusConflict)
		return
	}

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	if buf.code >= 400 {
		buf.flushTo(w)
		return
	}

	commitment := ReceiptCommitment(
		m.cfg.ChainID, spec.ServiceID, 0, sub.Buyer, spec.PayTo, units, 0, nonce, resHash,
	)
	m.mu.Lock()
	m.localSubUsage[subID] += units
	m.receipts = append(m.receipts, PendingReceipt{
		Rail:         "subscription",
		ServiceID:    spec.ServiceID,
		SubID:        subID,
		Buyer:        sub.Buyer,
		Provider:     spec.PayTo,
		Units:        units,
		AmountUgnot:  0,
		Nonce:        nonce,
		ResourceHash: resHash,
		Commitment:   commitment,
		CreatedAt:    time.Now().UTC(),
	})
	m.mu.Unlock()

	buf.header.Set(HeaderTollgateRail, "subscription")
	buf.header.Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.header.Set(HeaderTollgateNonce, nonce)
	buf.header.Set(HeaderTollgateResHash, resHash)
	buf.header.Set(HeaderTollgateCommitment, commitment)
	buf.header.Set(HeaderTollgateNetUgnot, "0")
	buf.flushTo(w)
}

func (m *Middleware) handlePass(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	spec RouteSpec,
	passID uint64,
	units int64,
	nonce string,
	resHash string,
) {
	if m.cfg.Chain == nil {
		m.write402(w, r, spec, units, nonce, "chain reader unavailable")
		return
	}
	pass, err := m.cfg.Chain.Pass(r.Context(), passID)
	if err != nil || pass.Closed || pass.ServiceID != spec.ServiceID {
		m.write402(w, r, spec, units, nonce, "credit pass not found, closed, or wrong service")
		return
	}
	if pass.CurrentHeight >= pass.ExpiryHeight {
		m.write402(w, r, spec, units, nonce, "credit pass expired")
		return
	}
	charge, err := ComputeCharge(spec.UnitPriceUgnot, units)
	if err != nil {
		http.Error(w, `{"error":"invalid charge computation"}`, http.StatusBadRequest)
		return
	}
	if charge.NetUgnot > pass.MaxPerCallUgnot*units {
		m.write402(w, r, spec, units, nonce, "charge exceeds pass maxPerCallUgnot ceiling")
		return
	}

	m.mu.Lock()
	localDebit := m.localDebits[passID]
	if pass.AvailableUgnot-localDebit < charge.NetUgnot {
		m.mu.Unlock()
		m.write402(w, r, spec, units, nonce, "insufficient available pass balance")
		return
	}
	m.mu.Unlock()

	if !m.reserveNonce(spec.ServiceID, nonce) {
		http.Error(w, `{"error":"duplicate nonce"}`, http.StatusConflict)
		return
	}

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	// Identical to x402 authorization semantics: never charge for a >=400 resource error.
	if buf.code >= 400 {
		buf.flushTo(w)
		return
	}

	commitment := ReceiptCommitment(
		m.cfg.ChainID, spec.ServiceID, passID, pass.Buyer, spec.PayTo,
		units, charge.NetUgnot, nonce, resHash,
	)

	m.mu.Lock()
	m.localDebits[passID] += charge.NetUgnot
	m.receipts = append(m.receipts, PendingReceipt{
		Rail:         "metered-pass",
		ServiceID:    spec.ServiceID,
		PassID:       passID,
		Buyer:        pass.Buyer,
		Provider:     spec.PayTo,
		Units:        units,
		GrossUgnot:   charge.GrossUgnot,
		DiscountBps:  charge.DiscountBps,
		AmountUgnot:  charge.NetUgnot,
		Nonce:        nonce,
		ResourceHash: resHash,
		Commitment:   commitment,
		CreatedAt:    time.Now().UTC(),
	})
	m.mu.Unlock()

	buf.header.Set(HeaderTollgateRail, "metered-pass")
	buf.header.Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.header.Set(HeaderTollgateNonce, nonce)
	buf.header.Set(HeaderTollgateResHash, resHash)
	buf.header.Set(HeaderTollgateCommitment, commitment)
	buf.header.Set(HeaderTollgateNetUgnot, strconv.FormatInt(charge.NetUgnot, 10))
	buf.flushTo(w)
}

func (m *Middleware) handleX402(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	spec RouteSpec,
	units int64,
	nonce string,
	resHash string,
	sigHeader string,
) {
	if m.cfg.Facilitator == nil {
		m.write402(w, r, spec, units, nonce, "x402 facilitator not configured")
		return
	}
	charge, err := ComputeCharge(spec.UnitPriceUgnot, units)
	if err != nil {
		http.Error(w, `{"error":"invalid charge"}`, http.StatusBadRequest)
		return
	}
	memo, _ := FormatInvoiceMemo(spec.ServiceID, nonce)
	reqTerms := PaymentRequirements{
		Scheme:            "exact",
		Network:           "gno:" + m.cfg.ChainID,
		Asset:             "ugnot",
		Amount:            strconv.FormatInt(charge.NetUgnot, 10),
		PayTo:             spec.PayTo,
		MaxTimeoutSeconds: 300,
		Extra: map[string]any{
			"areFeesSponsored": false,
			"memo":             memo,
		},
	}

	verifyRes, err := m.cfg.Facilitator.Verify(r.Context(), sigHeader, reqTerms)
	if err != nil || !verifyRes.IsValid {
		reason := verifyRes.InvalidReason
		if err != nil {
			reason = err.Error()
		}
		m.write402(w, r, spec, units, nonce, "x402 verify refused: "+reason)
		return
	}

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	if buf.code >= 400 {
		buf.flushTo(w)
		return
	}

	settleRes, err := m.cfg.Facilitator.Settle(r.Context(), sigHeader, reqTerms)
	if err != nil || !settleRes.Success {
		reason := settleRes.ErrorReason
		if err != nil {
			reason = err.Error()
		}
		m.write402(w, r, spec, units, nonce, "x402 settle failed: "+reason)
		return
	}

	payer := settleRes.Payer
	if payer == "" {
		payer = verifyRes.Payer
	}
	commitment := ReceiptCommitment(
		m.cfg.ChainID, spec.ServiceID, 0, payer, spec.PayTo,
		units, charge.NetUgnot, nonce, resHash,
	)

	m.mu.Lock()
	m.receipts = append(m.receipts, PendingReceipt{
		Rail:         "x402-direct",
		ServiceID:    spec.ServiceID,
		Buyer:        payer,
		Provider:     spec.PayTo,
		Units:        units,
		GrossUgnot:   charge.GrossUgnot,
		DiscountBps:  charge.DiscountBps,
		AmountUgnot:  charge.NetUgnot,
		Nonce:        nonce,
		ResourceHash: resHash,
		Commitment:   commitment,
		TxHash:       settleRes.Transaction,
		CreatedAt:    time.Now().UTC(),
	})
	m.mu.Unlock()

	payRespRaw, _ := json.Marshal(settleRes)
	buf.header.Set(HeaderPaymentResponse, base64.StdEncoding.EncodeToString(payRespRaw))
	buf.header.Set(HeaderTollgateRail, "x402-direct")
	buf.header.Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.header.Set(HeaderTollgateNonce, nonce)
	buf.header.Set(HeaderTollgateResHash, resHash)
	buf.header.Set(HeaderTollgateCommitment, commitment)
	buf.header.Set(HeaderTollgateNetUgnot, strconv.FormatInt(charge.NetUgnot, 10))
	buf.flushTo(w)
}

func (m *Middleware) write402(
	w http.ResponseWriter,
	r *http.Request,
	spec RouteSpec,
	units int64,
	nonce string,
	errMsg string,
) {
	charge, _ := ComputeCharge(spec.UnitPriceUgnot, units)
	memo, _ := FormatInvoiceMemo(spec.ServiceID, nonce)
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	resourceURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.RequestURI())
	mime := spec.MimeType
	if mime == "" {
		mime = "application/json"
	}

	pr := PaymentRequired{
		X402Version: 2,
		Error:       errMsg,
		Resource: X402Resource{
			URL:         resourceURL,
			Description: spec.Description,
			MimeType:    mime,
		},
		Accepts: []PaymentRequirements{{
			Scheme:            "exact",
			Network:           "gno:" + m.cfg.ChainID,
			Asset:             "ugnot",
			Amount:            strconv.FormatInt(charge.NetUgnot, 10),
			PayTo:             spec.PayTo,
			MaxTimeoutSeconds: 300,
			Extra: map[string]any{
				"areFeesSponsored": false,
				"memo":             memo,
				"tollgateRealm":    m.cfg.RealmPath,
				"tollgateService":  spec.ServiceID,
			},
		}},
	}

	encoded, _ := EncodePaymentRequired(pr)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderPaymentRequired, encoded)
	w.Header().Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	w.Header().Set(HeaderTollgateRealm, m.cfg.RealmPath)
	w.Header().Set(HeaderTollgateNonce, nonce)
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":          402,
		"error":           errMsg,
		"x402Version":     2,
		"paymentRequired": pr,
		"tollgateRails": map[string]any{
			"realmPath":      m.cfg.RealmPath,
			"serviceId":      spec.ServiceID,
			"passHeader":     HeaderTollgatePass,
			"subHeader":      HeaderTollgateSub,
			"x402Header":     HeaderPaymentSignature,
			"volumeDiscount": charge,
		},
	})
}
