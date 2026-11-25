// Package tollgate implements the on-chain HTTP 402 (x402) metering vault,
// prepaid credit passes, subscription quotas, challenge-window settlements,
// and cryptographic receipt anchoring for Gno.land.
package tollgate

import (
	"chain"
	"chain/banker"
	"chain/runtime"
	"chain/runtime/unsafe"
	"strconv"
	"strings"

	ptollgate "gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate"
	"gno.land/p/nt/markdown/sanitize/v0"
	"gno.land/p/nt/seqid/v0"
)

func mustCurrent(cur realm) {
	if !cur.IsCurrent() {
		panic("tollgate: spoofed realm capability")
	}
}

func mustDirectUserPayment(cur realm) int64 {
	mustCurrent(cur)
	if !cur.Previous().IsUserCall() {
		panic("tollgate: payment must be a direct user call")
	}
	// Paired with IsUserCall(), OriginSend reflects coins credited directly
	// to this realm in the top-level MsgCall envelope.
	sent := unsafe.OriginSend().AmountOf("ugnot")
	if sent <= 0 {
		panic("tollgate: positive ugnot payment required")
	}
	return sent
}

func idKey(id uint64) string {
	return seqid.ID(id).Binary()
}

func nonceKey(serviceID uint64, nonce string) string {
	return strconv.FormatUint(serviceID, 10) + ":" + nonce
}

func getServiceRecord(id uint64) *serviceRecord {
	if id == 0 {
		return nil
	}
	v := services.Get(idKey(id))
	if v == nil {
		return nil
	}
	return v.(*serviceRecord)
}

func getPassRecord(id uint64) *passRecord {
	if id == 0 {
		return nil
	}
	v := passes.Get(idKey(id))
	if v == nil {
		return nil
	}
	return v.(*passRecord)
}

func getSubRecord(id uint64) *subRecord {
	if id == 0 {
		return nil
	}
	v := subs.Get(idKey(id))
	if v == nil {
		return nil
	}
	return v.(*subRecord)
}

func getSettlementRecord(id uint64) *settlementRecord {
	if id == 0 {
		return nil
	}
	v := settlements.Get(idKey(id))
	if v == nil {
		return nil
	}
	return v.(*settlementRecord)
}

func getReceiptRecord(id uint64) *receiptRecord {
	if id == 0 {
		return nil
	}
	v := receipts.Get(idKey(id))
	if v == nil {
		return nil
	}
	return v.(*receiptRecord)
}

// RegisterService registers a new HTTP 402 / Tollgate metered API service.
func RegisterService(
	cur realm,
	slug string,
	name string,
	endpointURL string,
	payTo address,
	unitPriceUgnot int64,
	subPriceUgnot int64,
	subBlocks int64,
	subQuota int64,
	challengeBlocks int64,
	description string,
) uint64 {
	mustCurrent(cur)
	owner := cur.Previous().Address()

	slug = strings.TrimSpace(slug)
	if !ptollgate.ValidateSlug(slug) {
		panic("tollgate: invalid service slug (use 2..40 lowercase a-z0-9-)")
	}
	if slugIndex.Has(slug) {
		panic("tollgate: slug already registered")
	}
	name = strings.TrimSpace(sanitize.StripBidiAndZeroWidth(name))
	if name == "" || len(name) > maxNameLen {
		panic("tollgate: invalid service name")
	}
	endpointURL = strings.TrimSpace(sanitize.URL(endpointURL))
	if endpointURL == "" || len(endpointURL) > maxURLLen {
		panic("tollgate: invalid endpoint URL")
	}
	description = strings.TrimSpace(sanitize.StripBidiAndZeroWidth(description))
	if len(description) > maxDescLen {
		panic("tollgate: description exceeds max length")
	}
	if !payTo.IsValid() {
		payTo = owner
	}
	if unitPriceUgnot <= 0 {
		panic("tollgate: unitPriceUgnot must be positive")
	}
	if subPriceUgnot < 0 {
		panic("tollgate: subPriceUgnot cannot be negative")
	}
	if subPriceUgnot > 0 && (subBlocks < minExpiryBlocks || subQuota <= 0) {
		panic("tollgate: subscription requires valid subBlocks and subQuota")
	}
	if challengeBlocks < ptollgate.MinChallengeBlocks || challengeBlocks > ptollgate.MaxChallengeBlocks {
		panic("tollgate: challengeBlocks out of bounds")
	}

	sid := nextServiceID.Next()
	rec := &serviceRecord{
		id:              uint64(sid),
		slug:            slug,
		name:            name,
		description:     description,
		endpointURL:     endpointURL,
		owner:           owner,
		payTo:           payTo,
		unitPriceUgnot:  unitPriceUgnot,
		subPriceUgnot:   subPriceUgnot,
		subBlocks:       subBlocks,
		subQuota:        subQuota,
		challengeBlocks: challengeBlocks,
		paused:          false,
		createdHeight:   runtime.ChainHeight(),
	}
	services.Set(sid.Binary(), rec)
	slugIndex.Set(slug, uint64(sid))

	chain.Emit(
		"ServiceRegistered",
		"service_id", strconv.FormatUint(rec.id, 10),
		"slug", slug,
		"owner", owner.String(),
		"pay_to", payTo.String(),
		"unit_price_ugnot", strconv.FormatInt(unitPriceUgnot, 10),
	)
	return rec.id
}

// UpdateServicePricing lets the service owner adjust unit and subscription prices.
func UpdateServicePricing(
	cur realm,
	serviceID uint64,
	unitPriceUgnot int64,
	subPriceUgnot int64,
	subBlocks int64,
	subQuota int64,
) {
	mustCurrent(cur)
	svc := getServiceRecord(serviceID)
	if svc == nil {
		panic("tollgate: service not found")
	}
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may update pricing")
	}
	if unitPriceUgnot <= 0 || subPriceUgnot < 0 {
		panic("tollgate: invalid pricing parameters")
	}
	if subPriceUgnot > 0 && (subBlocks < minExpiryBlocks || subQuota <= 0) {
		panic("tollgate: subscription requires valid subBlocks and subQuota")
	}
	svc.unitPriceUgnot = unitPriceUgnot
	svc.subPriceUgnot = subPriceUgnot
	svc.subBlocks = subBlocks
	svc.subQuota = subQuota

	chain.Emit(
		"ServicePricingUpdated",
		"service_id", strconv.FormatUint(serviceID, 10),
		"unit_price_ugnot", strconv.FormatInt(unitPriceUgnot, 10),
		"sub_price_ugnot", strconv.FormatInt(subPriceUgnot, 10),
	)
}

// SetServicePaused pauses or unpauses new passes/subscriptions on a service.
func SetServicePaused(cur realm, serviceID uint64, paused bool) {
	mustCurrent(cur)
	svc := getServiceRecord(serviceID)
	if svc == nil {
		panic("tollgate: service not found")
	}
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may pause service")
	}
	svc.paused = paused
	state := StatusActive
	if paused {
		state = StatusPaused
	}
	chain.Emit("ServiceStatusChanged", "service_id", strconv.FormatUint(serviceID, 10), "status", state)
}

// OpenPass locks buyer ugnot into an on-chain prepaid credit vault for high-frequency
// metered API calls without per-call L1 latency or sequence-race exposure.
func OpenPass(cur realm, serviceID uint64, maxPerCallUgnot int64, expiryBlocks int64) uint64 {
	sent := mustDirectUserPayment(cur)
	svc := getServiceRecord(serviceID)
	if svc == nil {
		panic("tollgate: service not found")
	}
	if svc.paused {
		panic("tollgate: service is paused")
	}
	if expiryBlocks < minExpiryBlocks || expiryBlocks > maxExpiryBlocks {
		panic("tollgate: expiryBlocks out of bounds")
	}
	if maxPerCallUgnot <= 0 {
		maxPerCallUgnot = sent
	}
	if maxPerCallUgnot < svc.unitPriceUgnot {
		panic("tollgate: maxPerCallUgnot below service unit price")
	}

	buyer := cur.Previous().Address()
	sessAddr, _, _, isSess := runtime.GetSessionInfo()
	height := runtime.ChainHeight()

	pid := nextPassID.Next()
	rec := &passRecord{
		id:              uint64(pid),
		serviceID:       serviceID,
		buyer:           buyer,
		depositUgnot:    sent,
		maxPerCallUgnot: maxPerCallUgnot,
		expiryHeight:    height + expiryBlocks,
		openedHeight:    height,
		usedSessionKey:  isSess,
		sessionKeyAddr:  sessAddr,
	}
	passes.Set(pid.Binary(), rec)
	totalDepositedUgnot += sent

	chain.Emit(
		"PassOpened",
		"pass_id", strconv.FormatUint(rec.id, 10),
		"service_id", strconv.FormatUint(serviceID, 10),
		"buyer", buyer.String(),
		"deposit_ugnot", strconv.FormatInt(sent, 10),
		"expiry_height", strconv.FormatInt(rec.expiryHeight, 10),
	)
	return rec.id
}

// TopUpPass adds more ugnot and optionally extends expiry of an active pass.
func TopUpPass(cur realm, passID uint64, extendBlocks int64) {
	sent := mustDirectUserPayment(cur)
	p := getPassRecord(passID)
	if p == nil {
		panic("tollgate: pass not found")
	}
	if p.closed {
		panic("tollgate: pass is closed")
	}
	if cur.Previous().Address() != p.buyer {
		panic("tollgate: only pass buyer may top up")
	}
	if extendBlocks < 0 || extendBlocks > maxExpiryBlocks {
		panic("tollgate: invalid extendBlocks")
	}
	p.depositUgnot += sent
	if extendBlocks > 0 {
		base := p.expiryHeight
		if h := runtime.ChainHeight(); h > base {
			base = h
		}
		p.expiryHeight = base + extendBlocks
	}
	totalDepositedUgnot += sent

	chain.Emit(
		"PassToppedUp",
		"pass_id", strconv.FormatUint(passID, 10),
		"added_ugnot", strconv.FormatInt(sent, 10),
		"expiry_height", strconv.FormatInt(p.expiryHeight, 10),
	)
}

// RequestEarlyClose shortens a pass expiry to height + service.challengeBlocks so
// the provider gets one bounded settlement window and cannot lock buyer funds.
func RequestEarlyClose(cur realm, passID uint64) int64 {
	mustCurrent(cur)
	p := getPassRecord(passID)
	if p == nil {
		panic("tollgate: pass not found")
	}
	if p.closed {
		panic("tollgate: pass already closed")
	}
	if cur.Previous().Address() != p.buyer {
		panic("tollgate: only pass buyer may request close")
	}
	svc := getServiceRecord(p.serviceID)
	target := runtime.ChainHeight() + svc.challengeBlocks
	if target < p.expiryHeight {
		p.expiryHeight = target
	}
	chain.Emit(
		"PassEarlyCloseRequested",
		"pass_id", strconv.FormatUint(passID, 10),
		"expiry_height", strconv.FormatInt(p.expiryHeight, 10),
	)
	return p.expiryHeight
}

// ClosePass refunds any unspent, non-pending ugnot back to the pass buyer once
// the pass has expired (or its service is paused) and no settlement is pending.
func ClosePass(cur realm, passID uint64) int64 {
	mustCurrent(cur)
	p := getPassRecord(passID)
	if p == nil {
		panic("tollgate: pass not found")
	}
	if p.closed {
		panic("tollgate: pass already closed")
	}
	if cur.Previous().Address() != p.buyer {
		panic("tollgate: only pass buyer may close pass")
	}
	svc := getServiceRecord(p.serviceID)
	height := runtime.ChainHeight()
	if height < p.expiryHeight && !svc.paused {
		panic("tollgate: pass not expired yet (call RequestEarlyClose first)")
	}
	if p.pendingUgnot > 0 {
		panic("tollgate: pending settlements must resolve before closing")
	}

	refund, err := ptollgate.ComputeAvailable(p.depositUgnot, p.spentUgnot, p.pendingUgnot)
	if err != nil {
		panic(err.Error())
	}
	p.closed = true
	if refund > 0 {
		totalRefundedUgnot += refund
		b := banker.NewBanker(banker.BankerTypeRealmSend, cur)
		b.SendCoins(cur.Address(), p.buyer, chain.NewCoins(chain.NewCoin("ugnot", refund)))
	}

	chain.Emit(
		"PassClosed",
		"pass_id", strconv.FormatUint(passID, 10),
		"buyer", p.buyer.String(),
		"refund_ugnot", strconv.FormatInt(refund, 10),
	)
	return refund
}

// Subscribe purchases a fixed-window quota subscription and pays the service
// provider immediately via BankerTypeRealmSend.
func Subscribe(cur realm, serviceID uint64) uint64 {
	sent := mustDirectUserPayment(cur)
	svc := getServiceRecord(serviceID)
	if svc == nil {
		panic("tollgate: service not found")
	}
	if svc.paused {
		panic("tollgate: service is paused")
	}
	if svc.subPriceUgnot <= 0 {
		panic("tollgate: service does not offer subscriptions")
	}
	if sent < svc.subPriceUgnot {
		panic("tollgate: insufficient ugnot for subscription")
	}

	buyer := cur.Previous().Address()
	sessAddr, _, _, isSess := runtime.GetSessionInfo()
	height := runtime.ChainHeight()
	sid := nextSubID.Next()

	rec := &subRecord{
		id:             uint64(sid),
		serviceID:      serviceID,
		buyer:          buyer,
		paidUgnot:      svc.subPriceUgnot,
		quotaTotal:     svc.subQuota,
		quotaUsed:      0,
		startHeight:    height,
		expiryHeight:   height + svc.subBlocks,
		usedSessionKey: isSess,
		sessionKeyAddr: sessAddr,
	}
	subs.Set(sid.Binary(), rec)
	svc.totalRevenue += svc.subPriceUgnot
	totalSubRevenueUgnot += svc.subPriceUgnot

	b := banker.NewBanker(banker.BankerTypeRealmSend, cur)
	b.SendCoins(cur.Address(), svc.payTo, chain.NewCoins(chain.NewCoin("ugnot", svc.subPriceUgnot)))
	if change := sent - svc.subPriceUgnot; change > 0 {
		b.SendCoins(cur.Address(), buyer, chain.NewCoins(chain.NewCoin("ugnot", change)))
	}

	chain.Emit(
		"Subscribed",
		"sub_id", strconv.FormatUint(rec.id, 10),
		"service_id", strconv.FormatUint(serviceID, 10),
		"buyer", buyer.String(),
		"paid_ugnot", strconv.FormatInt(svc.subPriceUgnot, 10),
		"quota", strconv.FormatInt(svc.subQuota, 10),
		"expiry_height", strconv.FormatInt(rec.expiryHeight, 10),
	)
	return rec.id
}

// ConsumeSubscription records metered quota usage against an active subscription
// and anchors a verifiable SHA-256 usage receipt on chain.
func ConsumeSubscription(
	cur realm,
	subID uint64,
	units int64,
	nonce string,
	resourceHash string,
) uint64 {
	mustCurrent(cur)
	sub := getSubRecord(subID)
	if sub == nil {
		panic("tollgate: subscription not found")
	}
	svc := getServiceRecord(sub.serviceID)
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may consume subscription quota")
	}
	if runtime.ChainHeight() > sub.expiryHeight {
		panic("tollgate: subscription expired")
	}
	if units <= 0 || sub.quotaUsed+units > sub.quotaTotal {
		panic("tollgate: subscription quota exceeded")
	}
	if !ptollgate.ValidateNonce(nonce) {
		panic("tollgate: invalid nonce")
	}
	if !ptollgate.IsValidHexDigest(resourceHash) {
		panic("tollgate: invalid resourceHash (expect 64-char lowercase sha256 hex)")
	}
	nk := nonceKey(sub.serviceID, nonce)
	if nonceUsed.Has(nk) {
		panic("tollgate: duplicate nonce")
	}

	sub.quotaUsed += units
	svc.totalCalls += units

	commitment := ptollgate.ReceiptCommitment(
		runtime.ChainID(),
		sub.serviceID,
		0,
		sub.buyer.String(),
		svc.payTo.String(),
		units,
		0,
		nonce,
		resourceHash,
	)

	rid := nextReceiptID.Next()
	r := &receiptRecord{
		id:           uint64(rid),
		rail:         RailSubscription,
		serviceID:    sub.serviceID,
		subID:        sub.id,
		buyer:        sub.buyer,
		provider:     svc.payTo,
		units:        units,
		amountUgnot:  0,
		nonce:        nonce,
		resourceHash: resourceHash,
		commitment:   commitment,
		height:       runtime.ChainHeight(),
	}
	receipts.Set(rid.Binary(), r)
	receiptHashIndex.Set(commitment, r.id)
	nonceUsed.Set(nk, r.id)

	chain.Emit(
		"SubscriptionUsageAnchored",
		"receipt_id", strconv.FormatUint(r.id, 10),
		"sub_id", strconv.FormatUint(subID, 10),
		"units", strconv.FormatInt(units, 10),
		"commitment", commitment,
	)
	return r.id
}

// ClaimSettlement lets the service provider submit a metered usage batch against
// a prepaid pass. Funds move into pending escrow for challengeBlocks so the buyer
// can audit the SHA-256 commitment and dispute overcharges.
func ClaimSettlement(
	cur realm,
	passID uint64,
	units int64,
	nonce string,
	resourceHash string,
) uint64 {
	mustCurrent(cur)
	p := getPassRecord(passID)
	if p == nil {
		panic("tollgate: pass not found")
	}
	if p.closed {
		panic("tollgate: pass is closed")
	}
	svc := getServiceRecord(p.serviceID)
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may claim settlement")
	}
	if !ptollgate.ValidateNonce(nonce) {
		panic("tollgate: invalid nonce")
	}
	if !ptollgate.IsValidHexDigest(resourceHash) {
		panic("tollgate: invalid resourceHash (expect 64-char lowercase sha256 hex)")
	}
	nk := nonceKey(svc.id, nonce)
	if nonceUsed.Has(nk) {
		panic("tollgate: duplicate nonce")
	}

	charge, err := ptollgate.ComputeCharge(svc.unitPriceUgnot, units)
	if err != nil {
		panic(err.Error())
	}
	if charge.NetUgnot > p.maxPerCallUgnot*units {
		panic("tollgate: batch exceeds pass maxPerCallUgnot ceiling")
	}
	avail, err := ptollgate.ComputeAvailable(p.depositUgnot, p.spentUgnot, p.pendingUgnot)
	if err != nil {
		panic(err.Error())
	}
	if charge.NetUgnot > avail {
		panic("tollgate: insufficient available pass balance")
	}

	height := runtime.ChainHeight()
	commitment := ptollgate.ReceiptCommitment(
		runtime.ChainID(),
		svc.id,
		p.id,
		p.buyer.String(),
		svc.payTo.String(),
		units,
		charge.NetUgnot,
		nonce,
		resourceHash,
	)

	rid := nextReceiptID.Next()
	r := &receiptRecord{
		id:           uint64(rid),
		rail:         RailMeteredPass,
		serviceID:    svc.id,
		passID:       p.id,
		buyer:        p.buyer,
		provider:     svc.payTo,
		units:        units,
		amountUgnot:  charge.NetUgnot,
		nonce:        nonce,
		resourceHash: resourceHash,
		commitment:   commitment,
		height:       height,
	}
	receipts.Set(rid.Binary(), r)
	receiptHashIndex.Set(commitment, r.id)
	nonceUsed.Set(nk, r.id)

	stID := nextSettlementID.Next()
	st := &settlementRecord{
		id:            uint64(stID),
		serviceID:     svc.id,
		passID:        p.id,
		buyer:         p.buyer,
		provider:      svc.payTo,
		units:         units,
		grossUgnot:    charge.GrossUgnot,
		discountBps:   charge.DiscountBps,
		netUgnot:      charge.NetUgnot,
		nonce:         nonce,
		resourceHash:  resourceHash,
		commitment:    commitment,
		claimedHeight: height,
		challengeEnd:  height + svc.challengeBlocks,
		status:        StatusPending,
		receiptID:     r.id,
	}
	settlements.Set(stID.Binary(), st)
	p.pendingUgnot += charge.NetUgnot

	chain.Emit(
		"SettlementClaimed",
		"settlement_id", strconv.FormatUint(st.id, 10),
		"pass_id", strconv.FormatUint(p.id, 10),
		"receipt_id", strconv.FormatUint(r.id, 10),
		"net_ugnot", strconv.FormatInt(charge.NetUgnot, 10),
		"challenge_end", strconv.FormatInt(st.challengeEnd, 10),
		"commitment", commitment,
	)
	return st.id
}

// DisputeSettlement freezes a pending settlement inside its challenge window.
func DisputeSettlement(cur realm, settlementID uint64, reason string) {
	mustCurrent(cur)
	st := getSettlementRecord(settlementID)
	if st == nil {
		panic("tollgate: settlement not found")
	}
	if st.status != StatusPending {
		panic("tollgate: settlement is not pending")
	}
	if cur.Previous().Address() != st.buyer {
		panic("tollgate: only buyer may dispute settlement")
	}
	if runtime.ChainHeight() >= st.challengeEnd {
		panic("tollgate: challenge window has elapsed")
	}
	reason = strings.TrimSpace(sanitize.StripBidiAndZeroWidth(reason))
	if reason == "" || len(reason) > maxReasonLen {
		panic("tollgate: valid dispute reason required")
	}
	st.status = StatusDisputed
	st.disputeReason = reason

	chain.Emit(
		"SettlementDisputed",
		"settlement_id", strconv.FormatUint(settlementID, 10),
		"buyer", st.buyer.String(),
	)
}

// RefundDisputedSettlement lets the service owner concede a disputed or pending
// settlement, immediately releasing the locked ugnot back to the pass balance.
func RefundDisputedSettlement(cur realm, settlementID uint64) {
	mustCurrent(cur)
	st := getSettlementRecord(settlementID)
	if st == nil {
		panic("tollgate: settlement not found")
	}
	if st.status != StatusDisputed && st.status != StatusPending {
		panic("tollgate: settlement cannot be refunded")
	}
	svc := getServiceRecord(st.serviceID)
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may refund settlement")
	}
	p := getPassRecord(st.passID)
	p.pendingUgnot -= st.netUgnot
	st.status = StatusRefunded

	chain.Emit(
		"SettlementRefunded",
		"settlement_id", strconv.FormatUint(settlementID, 10),
		"pass_id", strconv.FormatUint(st.passID, 10),
		"amount_ugnot", strconv.FormatInt(st.netUgnot, 10),
	)
}

// FinalizeSettlement releases an undisputed settlement's locked ugnot to the
// service payTo address once the challenge window has passed.
func FinalizeSettlement(cur realm, settlementID uint64) {
	mustCurrent(cur)
	st := getSettlementRecord(settlementID)
	if st == nil {
		panic("tollgate: settlement not found")
	}
	if st.status != StatusPending {
		panic("tollgate: settlement is not pending")
	}
	if runtime.ChainHeight() < st.challengeEnd {
		panic("tollgate: challenge window still active")
	}

	p := getPassRecord(st.passID)
	svc := getServiceRecord(st.serviceID)

	p.pendingUgnot -= st.netUgnot
	p.spentUgnot += st.netUgnot
	p.settledCalls += st.units
	svc.totalCalls += st.units
	svc.totalRevenue += st.netUgnot
	totalSettledUgnot += st.netUgnot
	st.status = StatusFinalized

	b := banker.NewBanker(banker.BankerTypeRealmSend, cur)
	b.SendCoins(cur.Address(), svc.payTo, chain.NewCoins(chain.NewCoin("ugnot", st.netUgnot)))

	chain.Emit(
		"SettlementFinalized",
		"settlement_id", strconv.FormatUint(settlementID, 10),
		"pay_to", svc.payTo.String(),
		"net_ugnot", strconv.FormatInt(st.netUgnot, 10),
	)
}

// RecordX402Receipt anchors an external gnolang/x402 direct bank-send HTTP 402
// settlement on chain with canonical commitment + anti-replay nonce indexing.
func RecordX402Receipt(
	cur realm,
	serviceID uint64,
	buyer address,
	units int64,
	amountUgnot int64,
	nonce string,
	resourceHash string,
	txHash string,
) uint64 {
	mustCurrent(cur)
	svc := getServiceRecord(serviceID)
	if svc == nil {
		panic("tollgate: service not found")
	}
	if cur.Previous().Address() != svc.owner {
		panic("tollgate: only service owner may anchor x402 receipts")
	}
	if !buyer.IsValid() {
		panic("tollgate: invalid buyer address")
	}
	if units <= 0 || amountUgnot <= 0 {
		panic("tollgate: units and amountUgnot must be positive")
	}
	if !ptollgate.ValidateNonce(nonce) {
		panic("tollgate: invalid nonce")
	}
	if !ptollgate.IsValidHexDigest(resourceHash) {
		panic("tollgate: invalid resourceHash")
	}
	txHash = strings.TrimSpace(sanitize.StripBidiAndZeroWidth(txHash))
	if len(txHash) < 8 || len(txHash) > 128 {
		panic("tollgate: invalid txHash")
	}
	nk := nonceKey(serviceID, nonce)
	if nonceUsed.Has(nk) {
		panic("tollgate: duplicate nonce")
	}

	commitment := ptollgate.ReceiptCommitment(
		runtime.ChainID(),
		svc.id,
		0,
		buyer.String(),
		svc.payTo.String(),
		units,
		amountUgnot,
		nonce,
		resourceHash,
	)

	rid := nextReceiptID.Next()
	r := &receiptRecord{
		id:           uint64(rid),
		rail:         RailX402Direct,
		serviceID:    svc.id,
		buyer:        buyer,
		provider:     svc.payTo,
		units:        units,
		amountUgnot:  amountUgnot,
		nonce:        nonce,
		resourceHash: resourceHash,
		commitment:   commitment,
		txHash:       txHash,
		height:       runtime.ChainHeight(),
	}
	receipts.Set(rid.Binary(), r)
	receiptHashIndex.Set(commitment, r.id)
	nonceUsed.Set(nk, r.id)
	svc.totalCalls += units
	svc.totalRevenue += amountUgnot
	totalX402AnchoredUgnot += amountUgnot

	chain.Emit(
		"X402ReceiptAnchored",
		"receipt_id", strconv.FormatUint(r.id, 10),
		"service_id", strconv.FormatUint(serviceID, 10),
		"buyer", buyer.String(),
		"amount_ugnot", strconv.FormatInt(amountUgnot, 10),
		"tx_hash", txHash,
		"commitment", commitment,
	)
	return r.id
}

// --- Read-only query surface (value copies only, zero mutable pointers) ---

func GetService(serviceID uint64) (ServiceView, bool) {
	svc := getServiceRecord(serviceID)
	if svc == nil {
		return ServiceView{}, false
	}
	return ServiceView{
		ID:              svc.id,
		Slug:            svc.slug,
		Name:            svc.name,
		Description:     svc.description,
		EndpointURL:     svc.endpointURL,
		Owner:           svc.owner,
		PayTo:           svc.payTo,
		UnitPriceUgnot:  svc.unitPriceUgnot,
		SubPriceUgnot:   svc.subPriceUgnot,
		SubBlocks:       svc.subBlocks,
		SubQuota:        svc.subQuota,
		ChallengeBlocks: svc.challengeBlocks,
		Paused:          svc.paused,
		CreatedHeight:   svc.createdHeight,
		TotalCalls:      svc.totalCalls,
		TotalRevenue:    svc.totalRevenue,
	}, true
}

func GetServiceBySlug(slug string) (ServiceView, bool) {
	v := slugIndex.Get(slug)
	if v == nil {
		return ServiceView{}, false
	}
	return GetService(v.(uint64))
}

func GetPass(passID uint64) (PassView, bool) {
	p := getPassRecord(passID)
	if p == nil {
		return PassView{}, false
	}
	avail, _ := ptollgate.ComputeAvailable(p.depositUgnot, p.spentUgnot, p.pendingUgnot)
	return PassView{
		ID:              p.id,
		ServiceID:       p.serviceID,
		Buyer:           p.buyer,
		DepositUgnot:    p.depositUgnot,
		SpentUgnot:      p.spentUgnot,
		PendingUgnot:    p.pendingUgnot,
		AvailableUgnot:  avail,
		MaxPerCallUgnot: p.maxPerCallUgnot,
		ExpiryHeight:    p.expiryHeight,
		OpenedHeight:    p.openedHeight,
		Closed:          p.closed,
		UsedSessionKey:  p.usedSessionKey,
		SessionKeyAddr:  p.sessionKeyAddr,
		SettledCalls:    p.settledCalls,
	}, true
}

func GetSubscription(subID uint64) (SubscriptionView, bool) {
	s := getSubRecord(subID)
	if s == nil {
		return SubscriptionView{}, false
	}
	rem := s.quotaTotal - s.quotaUsed
	active := runtime.ChainHeight() <= s.expiryHeight && rem > 0
	return SubscriptionView{
		ID:             s.id,
		ServiceID:      s.serviceID,
		Buyer:          s.buyer,
		PaidUgnot:      s.paidUgnot,
		QuotaTotal:     s.quotaTotal,
		QuotaUsed:      s.quotaUsed,
		QuotaRemaining: rem,
		StartHeight:    s.startHeight,
		ExpiryHeight:   s.expiryHeight,
		Active:         active,
	}, true
}

func GetSettlement(settlementID uint64) (SettlementView, bool) {
	st := getSettlementRecord(settlementID)
	if st == nil {
		return SettlementView{}, false
	}
	return SettlementView{
		ID:            st.id,
		ServiceID:     st.serviceID,
		PassID:        st.passID,
		Buyer:         st.buyer,
		Provider:      st.provider,
		Units:         st.units,
		GrossUgnot:    st.grossUgnot,
		DiscountBps:   st.discountBps,
		NetUgnot:      st.netUgnot,
		Nonce:         st.nonce,
		ResourceHash:  st.resourceHash,
		Commitment:    st.commitment,
		ClaimedHeight: st.claimedHeight,
		ChallengeEnd:  st.challengeEnd,
		Status:        st.status,
		DisputeReason: st.disputeReason,
		ReceiptID:     st.receiptID,
	}, true
}

func GetReceipt(receiptID uint64) (ReceiptView, bool) {
	r := getReceiptRecord(receiptID)
	if r == nil {
		return ReceiptView{}, false
	}
	return ReceiptView{
		ID:           r.id,
		Rail:         r.rail,
		ServiceID:    r.serviceID,
		PassID:       r.passID,
		SubID:        r.subID,
		Buyer:        r.buyer,
		Provider:     r.provider,
		Units:        r.units,
		AmountUgnot:  r.amountUgnot,
		Nonce:        r.nonce,
		ResourceHash: r.resourceHash,
		Commitment:   r.commitment,
		TxHash:       r.txHash,
		Height:       r.height,
	}, true
}

// VerifyReceiptByCommitment checks whether a canonical SHA-256 receipt digest
// has been anchored on chain and returns its receipt ID.
func VerifyReceiptByCommitment(commitmentHex string) (uint64, bool) {
	if !ptollgate.IsValidHexDigest(commitmentHex) {
		return 0, false
	}
	v := receiptHashIndex.Get(commitmentHex)
	if v == nil {
		return 0, false
	}
	return v.(uint64), true
}

// CheckPassQuota is a fast read-only helper for HTTP gateways (via vm/qeval)
// verifying that a pass is open, unexpired, and covers a requested batch size.
func CheckPassQuota(passID uint64, units int64) bool {
	p := getPassRecord(passID)
	if p == nil || p.closed || units <= 0 {
		return false
	}
	if runtime.ChainHeight() >= p.expiryHeight {
		return false
	}
	svc := getServiceRecord(p.serviceID)
	if svc == nil || svc.paused {
		return false
	}
	charge, err := ptollgate.ComputeCharge(svc.unitPriceUgnot, units)
	if err != nil {
		return false
	}
	if charge.NetUgnot > p.maxPerCallUgnot*units {
		return false
	}
	avail, err := ptollgate.ComputeAvailable(p.depositUgnot, p.spentUgnot, p.pendingUgnot)
	if err != nil {
		return false
	}
	return avail >= charge.NetUgnot
}

// CheckSubscriptionActive verifies via vm/qeval whether a subscription has
// remaining quota and has not expired.
func CheckSubscriptionActive(subID uint64, units int64) bool {
	s := getSubRecord(subID)
	if s == nil || units <= 0 {
		return false
	}
	if runtime.ChainHeight() > s.expiryHeight {
		return false
	}
	return s.quotaUsed+units <= s.quotaTotal
}

// VaultBalanceUgnot returns the live ugnot locked in this realm using single-denom
// GetCoin (O(1) store read, immune to unsolicited-denom GetCoins gas DoS).
func VaultBalanceUgnot() int64 {
	pkgAddr := chain.PackageAddress(runtime.ChainDomain() + "/r/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate")
	return banker.NewReadonlyBanker().GetCoin(pkgAddr, "ugnot")
}
