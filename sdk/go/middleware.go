package tollgate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	BuyerSigHex  string    `json:"buyerSigHex,omitempty"`
	TxHash       string    `json:"txHash,omitempty"`
	Settled      bool      `json:"settled"`
	DeadLetter   bool      `json:"deadLetter,omitempty"`
	Attempts     int       `json:"attempts,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// SettlementSubmitter submits verified gateway receipts on-chain (ClaimSettlement,
// ConsumeSubscription, or RecordX402Receipt).
type SettlementSubmitter interface {
	SubmitReceipt(ctx context.Context, r PendingReceipt) (txHash string, err error)
}

type MiddlewareConfig struct {
	ChainID     string
	RealmPath   string
	Chain       ChainReader
	Facilitator Facilitator
	Submitter   SettlementSubmitter
	StateFile   string // optional JSON persistence for nonces, anchors & unsettled receipts
	RequireAuth bool   // when true, reject passes/subs lacking a PayWord SHA-256 voucher root
	Routes      map[string]RouteSpec
}

type persistedState struct {
	UsedNonces      map[string]bool   `json:"usedNonces"`
	Receipts        []PendingReceipt  `json:"receipts"`
	LocalPassAnchor map[uint64]string `json:"localPassAnchor,omitempty"`
	LocalSubAnchor  map[uint64]string `json:"localSubAnchor,omitempty"`
}

type Middleware struct {
	cfg             MiddlewareConfig
	mu              sync.Mutex
	flushMu         sync.Mutex
	passLocks       map[uint64]*sync.Mutex
	subLocks        map[uint64]*sync.Mutex
	usedNonces      map[string]bool
	localDebits     map[uint64]int64
	localSubUsage   map[uint64]int64
	localPassAnchor map[uint64]string
	localSubAnchor  map[uint64]string
	receipts        []PendingReceipt
}

func NewMiddleware(cfg MiddlewareConfig) *Middleware {
	m := &Middleware{
		cfg:             cfg,
		passLocks:       make(map[uint64]*sync.Mutex),
		subLocks:        make(map[uint64]*sync.Mutex),
		usedNonces:      make(map[string]bool),
		localDebits:     make(map[uint64]int64),
		localSubUsage:   make(map[uint64]int64),
		localPassAnchor: make(map[uint64]string),
		localSubAnchor:  make(map[uint64]string),
	}
	m.loadState()
	return m
}

// lockPass serializes reserve -> handler -> receipt/rollback for a single passID
// so PayWord hash-chain proofs always append and settle strictly in chain order,
// while distinct passes remain fully concurrent.
func (m *Middleware) lockPass(passID uint64) *sync.Mutex {
	m.mu.Lock()
	lk := m.passLocks[passID]
	if lk == nil {
		lk = &sync.Mutex{}
		m.passLocks[passID] = lk
	}
	m.mu.Unlock()
	lk.Lock()
	return lk
}

// lockSub serializes reserve -> handler -> receipt/rollback for a single subID.
func (m *Middleware) lockSub(subID uint64) *sync.Mutex {
	m.mu.Lock()
	lk := m.subLocks[subID]
	if lk == nil {
		lk = &sync.Mutex{}
		m.subLocks[subID] = lk
	}
	m.mu.Unlock()
	lk.Lock()
	return lk
}

func (m *Middleware) loadState() {
	if m.cfg.StateFile == "" {
		return
	}
	raw, err := os.ReadFile(m.cfg.StateFile)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return
	}
	for k, v := range st.UsedNonces {
		if v {
			m.usedNonces[k] = true
		}
	}
	for k, v := range st.LocalPassAnchor {
		m.localPassAnchor[k] = v
	}
	for k, v := range st.LocalSubAnchor {
		m.localSubAnchor[k] = v
	}
	m.receipts = st.Receipts
	for _, r := range m.receipts {
		if !r.Settled && !r.DeadLetter {
			if r.Rail == "metered-pass" && r.PassID > 0 {
				m.localDebits[r.PassID] += r.AmountUgnot
			} else if r.Rail == "subscription" && r.SubID > 0 {
				m.localSubUsage[r.SubID] += r.Units
			}
		}
	}
}

func (m *Middleware) saveStateLocked() {
	if m.cfg.StateFile == "" {
		return
	}
	raw, err := json.MarshalIndent(persistedState{
		UsedNonces:      m.usedNonces,
		Receipts:        m.receipts,
		LocalPassAnchor: m.localPassAnchor,
		LocalSubAnchor:  m.localSubAnchor,
	}, "", "  ")
	if err != nil {
		log.Printf("tollgate: state marshal error: %v", err)
		return
	}
	tmp := m.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		log.Printf("tollgate: state write error (%s): %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, m.cfg.StateFile); err != nil {
		log.Printf("tollgate: state rename error (%s): %v", m.cfg.StateFile, err)
	}
}

func (m *Middleware) Receipts() []PendingReceipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PendingReceipt, len(m.receipts))
	copy(out, m.receipts)
	return out
}

func isPermanentSettlementError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pat := range []string{
		"already used", "expired", "is closed", "invalid buyer",
		"not found", "invalid payword", "out of bounds", "exceeds",
	} {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}

func isTerminalChainError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pat := range []string{"expired", "is closed", "not found"} {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}

// FlushSettlements submits unsettled receipts in strict chain order per pass/sub.
// Transient RPC errors pause only that pass/sub for retry on the next tick (never
// dead-lettering a live PayWord chain). Permanent errors reconcile against the live
// on-chain anchor (recovering from crash-duplicate nonces or dead-lettering terminal
// expired/closed passes cleanly without blocking other passes).
func (m *Middleware) FlushSettlements(ctx context.Context) (int, error) {
	if m.cfg.Submitter == nil {
		return 0, nil
	}
	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	m.mu.Lock()
	var todo []int
	for i := range m.receipts {
		if !m.receipts[i].Settled && !m.receipts[i].DeadLetter {
			todo = append(todo, i)
		}
	}
	m.mu.Unlock()

	settledCount := 0
	var firstErr error
	pausedPass := make(map[uint64]bool)
	pausedSub := make(map[uint64]bool)

	for _, idx := range todo {
		m.mu.Lock()
		if m.receipts[idx].Settled || m.receipts[idx].DeadLetter {
			m.mu.Unlock()
			continue
		}
		rec := m.receipts[idx]
		m.mu.Unlock()

		if rec.Rail == "metered-pass" && pausedPass[rec.PassID] {
			continue
		}
		if rec.Rail == "subscription" && pausedSub[rec.SubID] {
			continue
		}

		txHash, err := m.cfg.Submitter.SubmitReceipt(ctx, rec)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			perm := isPermanentSettlementError(err)

			// Crash-recovery check: if an already-broadcast PayWord receipt returns
			// "already used" / "invalid payword", verify whether on-chain state already
			// moved to rec.BuyerSigHex (or whether the chain diverged).
			recovered := false
			if perm && m.cfg.Chain != nil && rec.BuyerSigHex != "" {
				if rec.Rail == "metered-pass" && rec.PassID > 0 {
					if p, cerr := m.cfg.Chain.Pass(ctx, rec.PassID); cerr == nil {
						if p.VoucherPubKey == rec.BuyerSigHex {
							recovered = true
						} else if !isTerminalChainError(err) {
							m.reconcilePassAnchorLocked(rec.PassID, p.VoucherPubKey)
						}
					}
				} else if rec.Rail == "subscription" && rec.SubID > 0 {
					if s, cerr := m.cfg.Chain.Subscription(ctx, rec.SubID); cerr == nil {
						if s.VoucherPubKey == rec.BuyerSigHex {
							recovered = true
						} else if !isTerminalChainError(err) {
							m.reconcileSubAnchorLocked(rec.SubID, s.VoucherPubKey)
						}
					}
				}
			}

			if recovered {
				m.mu.Lock()
				if !m.receipts[idx].Settled {
					m.receipts[idx].Settled = true
					m.receipts[idx].LastError = ""
					m.releaseReservationLocked(rec)
					m.saveStateLocked()
					settledCount++
				}
				m.mu.Unlock()
				continue
			}

			m.mu.Lock()
			m.receipts[idx].Attempts++
			m.receipts[idx].LastError = err.Error()

			// Never dead-letter a PayWord receipt on transient RPC/network errors;
			// pause this pass/sub for the current flush so chain ordering stays intact.
			isPayWordRail := (rec.Rail == "metered-pass" || rec.Rail == "subscription") && rec.BuyerSigHex != ""
			shouldDeadLetter := perm || (!isPayWordRail && m.receipts[idx].Attempts >= 3)

			if shouldDeadLetter {
				m.receipts[idx].DeadLetter = true
				m.releaseReservationLocked(rec)
				if isTerminalChainError(err) {
					// If pass/sub is closed or expired, clear local anchor & dead-letter
					// any remaining queued receipts for the same pass/sub.
					if rec.Rail == "metered-pass" {
						delete(m.localPassAnchor, rec.PassID)
					} else if rec.Rail == "subscription" {
						delete(m.localSubAnchor, rec.SubID)
					}
				}
			} else {
				if rec.Rail == "metered-pass" {
					pausedPass[rec.PassID] = true
				} else if rec.Rail == "subscription" {
					pausedSub[rec.SubID] = true
				}
			}
			m.saveStateLocked()
			m.mu.Unlock()
			continue
		}

		m.mu.Lock()
		if !m.receipts[idx].Settled {
			m.receipts[idx].Settled = true
			m.receipts[idx].LastError = ""
			if txHash != "" {
				m.receipts[idx].TxHash = txHash
			}
			m.releaseReservationLocked(rec)
			m.saveStateLocked()
			settledCount++
		}
		m.mu.Unlock()
	}
	return settledCount, firstErr
}

func (m *Middleware) releaseReservationLocked(rec PendingReceipt) {
	if rec.Rail == "metered-pass" && m.localDebits[rec.PassID] >= rec.AmountUgnot {
		m.localDebits[rec.PassID] -= rec.AmountUgnot
	}
	if rec.Rail == "subscription" && m.localSubUsage[rec.SubID] >= rec.Units {
		m.localSubUsage[rec.SubID] -= rec.Units
	}
}

func (m *Middleware) reconcilePassAnchorLocked(passID uint64, chainAnchor string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if chainAnchor == "" {
		delete(m.localPassAnchor, passID)
	} else {
		m.localPassAnchor[passID] = chainAnchor
	}
	m.saveStateLocked()
}

func (m *Middleware) reconcileSubAnchorLocked(subID uint64, chainAnchor string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if chainAnchor == "" {
		delete(m.localSubAnchor, subID)
	} else {
		m.localSubAnchor[subID] = chainAnchor
	}
	m.saveStateLocked()
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
			parsed, err := strconv.ParseInt(rawUnits, 10, 64)
			if err != nil || parsed <= 0 || parsed > MaxPayWordSteps {
				http.Error(w, fmt.Sprintf(`{"error":"X-Tollgate-Units must be between 1 and %d"}`, MaxPayWordSteps), http.StatusBadRequest)
				return
			}
			units = parsed
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

		// Rail 1: Subscription fast-path (PayWord SHA-256 hash-chain authenticated)
		if rawSub := r.Header.Get(HeaderTollgateSub); rawSub != "" {
			subID, err := strconv.ParseUint(rawSub, 10, 64)
			if err != nil || subID == 0 {
				m.write402(w, r, spec, units, nonce, "invalid X-Tollgate-Sub")
				return
			}
			m.handleSubscription(w, r, next, spec, subID, units, nonce, resHash)
			return
		}

		// Rail 2: Prepaid Credit Pass fast-path (PayWord SHA-256 hash-chain authenticated)
		if rawPass := r.Header.Get(HeaderTollgatePass); rawPass != "" {
			passID, err := strconv.ParseUint(rawPass, 10, 64)
			if err != nil || passID == 0 {
				m.write402(w, r, spec, units, nonce, "invalid X-Tollgate-Pass")
				return
			}
			m.handlePass(w, r, next, spec, passID, units, nonce, resHash)
			return
		}

		// Rail 3: Stock gnolang/x402 direct payment (extracts accepted.extra.memo automatically!)
		if sig := r.Header.Get(HeaderPaymentSignature); sig != "" {
			m.handleX402(w, r, next, spec, units, nonce, resHash, sig)
			return
		}

		m.write402(w, r, spec, units, nonce, "Payment required")
	})
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

	// Serialize per subID across reserve -> handler -> receipt/rollback
	subLk := m.lockSub(subID)
	defer subLk.Unlock()

	sub, err := m.cfg.Chain.Subscription(r.Context(), subID)
	if err != nil || !sub.Active || sub.ServiceID != spec.ServiceID {
		m.write402(w, r, spec, units, nonce, "subscription inactive or wrong service")
		return
	}

	buyerSig := r.Header.Get(HeaderTollgateVoucherSig)
	if sub.VoucherPubKey == "" && m.cfg.RequireAuth {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"subscription has no registered PayWord voucher root"}`))
		return
	}

	nKey := strconv.FormatUint(spec.ServiceID, 10) + ":" + nonce
	m.mu.Lock()
	if m.usedNonces[nKey] {
		m.mu.Unlock()
		http.Error(w, `{"error":"duplicate nonce"}`, http.StatusConflict)
		return
	}
	var prevSubAnchor string
	var advancedSubAnchor bool
	if sub.VoucherPubKey != "" {
		curAnchor := m.localSubAnchor[subID]
		if curAnchor == "" {
			curAnchor = sub.VoucherPubKey
		}
		if !VerifyPayWord(m.cfg.ChainID, curAnchor, buyerSig, units) {
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or missing PayWord X-Tollgate-Voucher-Sig for subscription"}`))
			return
		}
		prevSubAnchor = m.localSubAnchor[subID]
		m.localSubAnchor[subID] = buyerSig
		advancedSubAnchor = true
	}
	if units > sub.QuotaRemaining-m.localSubUsage[subID] {
		if advancedSubAnchor {
			if prevSubAnchor == "" {
				delete(m.localSubAnchor, subID)
			} else {
				m.localSubAnchor[subID] = prevSubAnchor
			}
		}
		m.mu.Unlock()
		m.write402(w, r, spec, units, nonce, "subscription quota exhausted")
		return
	}
	m.usedNonces[nKey] = true
	m.localSubUsage[subID] += units
	m.mu.Unlock()

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	if buf.code >= 400 {
		m.mu.Lock()
		m.localSubUsage[subID] -= units
		delete(m.usedNonces, nKey)
		if advancedSubAnchor {
			if prevSubAnchor == "" {
				delete(m.localSubAnchor, subID)
			} else {
				m.localSubAnchor[subID] = prevSubAnchor
			}
		}
		m.mu.Unlock()
		buf.flushTo(w)
		return
	}

	commitment := ReceiptCommitment(
		m.cfg.ChainID, spec.ServiceID, 0, sub.Buyer, spec.PayTo, units, 0, nonce, resHash,
	)
	m.mu.Lock()
	m.receipts = append(m.receipts, PendingReceipt{
		Rail:         "subscription",
		ServiceID:    spec.ServiceID,
		SubID:        subID,
		Buyer:        sub.Buyer,
		Provider:     spec.PayTo,
		Units:        units,
		Nonce:        nonce,
		ResourceHash: resHash,
		Commitment:   commitment,
		BuyerSigHex:  buyerSig,
		CreatedAt:    time.Now().UTC(),
	})
	m.saveStateLocked()
	m.mu.Unlock()

	buf.Header().Set(HeaderTollgateRail, "subscription")
	buf.Header().Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.Header().Set(HeaderTollgateRealm, m.cfg.RealmPath)
	buf.Header().Set(HeaderTollgateNonce, nonce)
	buf.Header().Set(HeaderTollgateResHash, resHash)
	buf.Header().Set(HeaderTollgateNetUgnot, "0")
	buf.Header().Set(HeaderTollgateCommitment, commitment)
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

	// Serialize per passID across reserve -> handler -> receipt/rollback
	passLk := m.lockPass(passID)
	defer passLk.Unlock()

	pass, err := m.cfg.Chain.Pass(r.Context(), passID)
	if err != nil || pass.Closed || pass.ServiceID != spec.ServiceID {
		m.write402(w, r, spec, units, nonce, "credit pass not found, closed, or wrong service")
		return
	}
	if pass.CurrentHeight >= pass.ExpiryHeight {
		m.write402(w, r, spec, units, nonce, "credit pass expired")
		return
	}

	buyerSig := r.Header.Get(HeaderTollgateVoucherSig)
	if pass.VoucherPubKey == "" && m.cfg.RequireAuth {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"pass has no registered PayWord voucher root"}`))
		return
	}

	charge, err := ComputeCharge(spec.UnitPriceUgnot, units)
	if err != nil {
		http.Error(w, `{"error":"invalid charge computation"}`, http.StatusBadRequest)
		return
	}
	if pass.MaxPerCallUgnot <= 0 || (pass.MaxPerCallUgnot <= math.MaxInt64/units && charge.NetUgnot > pass.MaxPerCallUgnot*units) {
		m.write402(w, r, spec, units, nonce, "charge exceeds pass maxPerCallUgnot ceiling")
		return
	}

	nKey := strconv.FormatUint(spec.ServiceID, 10) + ":" + nonce
	m.mu.Lock()
	if m.usedNonces[nKey] {
		m.mu.Unlock()
		http.Error(w, `{"error":"duplicate nonce"}`, http.StatusConflict)
		return
	}
	var prevPassAnchor string
	var advancedPassAnchor bool
	if pass.VoucherPubKey != "" {
		curAnchor := m.localPassAnchor[passID]
		if curAnchor == "" {
			curAnchor = pass.VoucherPubKey
		}
		if !VerifyPayWord(m.cfg.ChainID, curAnchor, buyerSig, units) {
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or missing PayWord X-Tollgate-Voucher-Sig for pass"}`))
			return
		}
		prevPassAnchor = m.localPassAnchor[passID]
		m.localPassAnchor[passID] = buyerSig
		advancedPassAnchor = true
	}
	if pass.AvailableUgnot-m.localDebits[passID] < charge.NetUgnot {
		if advancedPassAnchor {
			if prevPassAnchor == "" {
				delete(m.localPassAnchor, passID)
			} else {
				m.localPassAnchor[passID] = prevPassAnchor
			}
		}
		m.mu.Unlock()
		m.write402(w, r, spec, units, nonce, "insufficient available pass balance")
		return
	}
	m.usedNonces[nKey] = true
	m.localDebits[passID] += charge.NetUgnot
	m.mu.Unlock()

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	if buf.code >= 400 {
		m.mu.Lock()
		m.localDebits[passID] -= charge.NetUgnot
		delete(m.usedNonces, nKey)
		if advancedPassAnchor {
			if prevPassAnchor == "" {
				delete(m.localPassAnchor, passID)
			} else {
				m.localPassAnchor[passID] = prevPassAnchor
			}
		}
		m.mu.Unlock()
		buf.flushTo(w)
		return
	}

	commitment := ReceiptCommitment(
		m.cfg.ChainID, spec.ServiceID, passID, pass.Buyer, spec.PayTo,
		units, charge.NetUgnot, nonce, resHash,
	)
	m.mu.Lock()
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
		BuyerSigHex:  buyerSig,
		CreatedAt:    time.Now().UTC(),
	})
	m.saveStateLocked()
	m.mu.Unlock()

	buf.Header().Set(HeaderTollgateRail, "metered-pass")
	buf.Header().Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.Header().Set(HeaderTollgateRealm, m.cfg.RealmPath)
	buf.Header().Set(HeaderTollgateNonce, nonce)
	buf.Header().Set(HeaderTollgateResHash, resHash)
	buf.Header().Set(HeaderTollgateNetUgnot, strconv.FormatInt(charge.NetUgnot, 10))
	buf.Header().Set(HeaderTollgateCommitment, commitment)
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
	paymentSigHeader string,
) {
	if m.cfg.Facilitator == nil {
		m.write402(w, r, spec, units, nonce, "x402 facilitator not configured")
		return
	}
	charge, err := ComputeCharge(spec.UnitPriceUgnot, units)
	if err != nil {
		http.Error(w, `{"error":"invalid units"}`, http.StatusBadRequest)
		return
	}

	// Stock x402 client interop: if X-Tollgate-Nonce wasn't explicitly sent,
	// recover the invoice nonce from accepted.extra.memo in PAYMENT-SIGNATURE.
	if r.Header.Get(HeaderTollgateNonce) == "" {
		if memo, ok := ExtractX402AcceptedMemo(paymentSigHeader); ok && memo != "" {
			if svcID, parsedNonce, ok := ParseInvoiceMemo(memo); ok && svcID == spec.ServiceID {
				nonce = parsedNonce
			}
		}
	}

	memo, err := FormatInvoiceMemo(spec.ServiceID, nonce)
	if err != nil {
		http.Error(w, `{"error":"invalid nonce"}`, http.StatusBadRequest)
		return
	}

	nKey := strconv.FormatUint(spec.ServiceID, 10) + ":" + nonce
	m.mu.Lock()
	if m.usedNonces[nKey] {
		m.mu.Unlock()
		http.Error(w, `{"error":"duplicate x402 invoice nonce"}`, http.StatusConflict)
		return
	}
	m.usedNonces[nKey] = true
	m.mu.Unlock()

	releaseNonce := func() {
		m.mu.Lock()
		delete(m.usedNonces, nKey)
		m.mu.Unlock()
	}

	reqSpec := PaymentRequirements{
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
	}

	vres, err := m.cfg.Facilitator.Verify(r.Context(), paymentSigHeader, reqSpec)
	if err != nil || !vres.IsValid {
		releaseNonce()
		reason := "x402 verification failed"
		if err != nil {
			reason = err.Error()
		} else if vres.InvalidReason != "" {
			reason = vres.InvalidReason
		}
		m.write402(w, r, spec, units, nonce, reason)
		return
	}

	buf := newBufferedResponse()
	next.ServeHTTP(buf, r)
	if buf.code >= 400 {
		releaseNonce()
		buf.flushTo(w)
		return
	}

	sres, err := m.cfg.Facilitator.Settle(r.Context(), paymentSigHeader, reqSpec)
	if err != nil || !sres.Success {
		releaseNonce()
		reason := "x402 settlement failed"
		if err != nil {
			reason = err.Error()
		} else if sres.ErrorReason != "" {
			reason = sres.ErrorReason
		}
		m.write402(w, r, spec, units, nonce, reason)
		return
	}

	payer := sres.Payer
	if payer == "" {
		payer = vres.Payer
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
		TxHash:       sres.Transaction,
		CreatedAt:    time.Now().UTC(),
	})
	m.saveStateLocked()
	m.mu.Unlock()

	payRespJSON, _ := json.Marshal(sres)
	buf.Header().Set(HeaderPaymentResponse, base64.StdEncoding.EncodeToString(payRespJSON))
	buf.Header().Set(HeaderTollgateRail, "x402-direct")
	buf.Header().Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	buf.Header().Set(HeaderTollgateRealm, m.cfg.RealmPath)
	buf.Header().Set(HeaderTollgateNonce, nonce)
	buf.Header().Set(HeaderTollgateResHash, resHash)
	buf.Header().Set(HeaderTollgateNetUgnot, strconv.FormatInt(charge.NetUgnot, 10))
	buf.Header().Set(HeaderTollgateCommitment, commitment)
	buf.flushTo(w)
}

func (m *Middleware) write402(
	w http.ResponseWriter,
	r *http.Request,
	spec RouteSpec,
	units int64,
	nonce string,
	reason string,
) {
	charge, err := ComputeCharge(spec.UnitPriceUgnot, units)
	if err != nil {
		charge = ChargeBreakdown{Units: 1, UnitPrice: spec.UnitPriceUgnot, GrossUgnot: spec.UnitPriceUgnot, NetUgnot: spec.UnitPriceUgnot}
	}
	memo, _ := FormatInvoiceMemo(spec.ServiceID, nonce)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.RequestURI())
	pr := PaymentRequired{
		X402Version: 2,
		Error:       reason,
		Resource: X402Resource{
			URL:         fullURL,
			Description: spec.Description,
			MimeType:    spec.MimeType,
		},
		Accepts: []PaymentRequirements{
			{
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
			},
		},
	}
	encoded, _ := EncodePaymentRequired(pr)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderPaymentRequired, encoded)
	w.Header().Set(HeaderTollgateRealm, m.cfg.RealmPath)
	w.Header().Set(HeaderTollgateServiceID, strconv.FormatUint(spec.ServiceID, 10))
	w.Header().Set(HeaderTollgateNonce, nonce)
	w.Header().Set(HeaderTollgateNetUgnot, strconv.FormatInt(charge.NetUgnot, 10))
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":             reason,
		"x402Version":       2,
		"tollgateRealm":     m.cfg.RealmPath,
		"tollgateServiceId": spec.ServiceID,
		"units":             charge.Units,
		"grossUgnot":        charge.GrossUgnot,
		"discountBps":       charge.DiscountBps,
		"netUgnot":          charge.NetUgnot,
		"memo":              memo,
		"acceptedRails":     []string{"X-Tollgate-Sub", "X-Tollgate-Pass", "PAYMENT-SIGNATURE (gnolang/x402)"},
	})
}
