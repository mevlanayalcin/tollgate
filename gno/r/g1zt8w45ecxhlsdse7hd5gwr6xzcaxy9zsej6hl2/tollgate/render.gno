package tollgate

import (
	"chain/runtime"
	"strconv"
	"strings"

	ptollgate "gno.land/p/g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2/tollgate"
	"gno.land/p/nt/markdown/sanitize/v0"
	"gno.land/p/nt/ufmt/v0"
)

func jsonEscape(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(" ")
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Render serves a fully sanitized native gnoweb Markdown explorer plus
// machine-readable JSON routes (/json/summary, /json/services, /json/service/<id>,
// /json/pass/<id>, /json/sub/<id>, /json/receipt/<id>, /json/settlement/<id>).
func Render(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return renderHome()
	}

	parts := strings.Split(path, "/")
	switch parts[0] {
	case "services":
		return renderServices()
	case "service":
		if len(parts) == 2 {
			return renderServiceDetail(parts[1])
		}
	case "pass":
		if len(parts) == 2 {
			return renderPassDetail(parts[1])
		}
	case "sub":
		if len(parts) == 2 {
			return renderSubDetail(parts[1])
		}
	case "receipt":
		if len(parts) == 2 {
			return renderReceiptDetail(parts[1])
		}
	case "disputes":
		return renderDisputes()
	case "json":
		return renderJSON(parts[1:])
	}

	return "# Tollgate — Not Found\n\nUnknown route " + sanitize.InlineCode(path) + ".\n\n[Back to Tollgate Home](:)\n"
}

func renderHome() string {
	var b strings.Builder
	b.WriteString("# Tollgate — On-Chain x402 Metering & Receipt Protocol\n\n")
	b.WriteString("> Bridging `gnolang/x402` HTTP 402 micropayments with prepaid on-chain credit vaults, fixed-window subscriptions, challenge-window settlements, and verifiable SHA-256 receipts on Gno.land.\n\n")

	b.WriteString("## Live Protocol Telemetry\n\n")
	b.WriteString("| Metric | Value |\n|---|---|\n")
	b.WriteString(ufmt.Sprintf("| Chain ID | `%s` |\n", sanitize.InlineText(runtime.ChainID())))
	b.WriteString(ufmt.Sprintf("| Block Height | `%d` |\n", runtime.ChainHeight()))
	b.WriteString(ufmt.Sprintf("| Vault TVL (Live) | `%d ugnot` |\n", VaultBalanceUgnot()))
	b.WriteString(ufmt.Sprintf("| Registered Services | `%d` |\n", services.Size()))
	b.WriteString(ufmt.Sprintf("| Active / Total Passes | `%d` |\n", passes.Size()))
	b.WriteString(ufmt.Sprintf("| Subscriptions | `%d` |\n", subs.Size()))
	b.WriteString(ufmt.Sprintf("| Verifiable Receipts | `%d` |\n", receipts.Size()))
	b.WriteString(ufmt.Sprintf("| Total Deposited | `%d ugnot` |\n", totalDepositedUgnot))
	b.WriteString(ufmt.Sprintf("| Total Settled (Passes) | `%d ugnot` |\n", totalSettledUgnot))
	b.WriteString(ufmt.Sprintf("| Total x402 Anchored | `%d ugnot` |\n", totalX402AnchoredUgnot))
	b.WriteString(ufmt.Sprintf("| Total Subscriptions | `%d ugnot` |\n", totalSubRevenueUgnot))
	b.WriteString(ufmt.Sprintf("| Total Refunded | `%d ugnot` |\n\n", totalRefundedUgnot))

	b.WriteString("## Registered Services\n\n")
	if services.Size() == 0 {
		b.WriteString("_No services registered yet._\n\n")
	} else {
		b.WriteString("| ID | Slug | Name | Unit Price | Sub Plan | Calls | Revenue | Status |\n|---|---|---|---|---|---|---|---|\n")
		services.ReverseIterateByOffset(0, maxRenderRows, func(_ string, v any) bool {
			s := v.(*serviceRecord)
			status := StatusActive
			if s.paused {
				status = StatusPaused
			}
			subStr := "—"
			if s.subPriceUgnot > 0 {
				subStr = ufmt.Sprintf("%d ugnot / %d calls", s.subPriceUgnot, s.subQuota)
			}
			b.WriteString(ufmt.Sprintf(
				"| [%d](:service/%d) | `%s` | %s | `%d ugnot` | %s | `%d` | `%d ugnot` | `%s` |\n",
				s.id,
				s.id,
				sanitize.TableCell(s.slug),
				sanitize.TableCell(s.name),
				s.unitPriceUgnot,
				subStr,
				s.totalCalls,
				s.totalRevenue,
				status,
			))
			return false
		})
		b.WriteString("\n")
	}

	b.WriteString("## Recent Verifiable Receipts\n\n")
	if receipts.Size() == 0 {
		b.WriteString("_No receipts anchored yet._\n\n")
	} else {
		b.WriteString("| ID | Rail | Service | Units | Amount | Commitment (SHA-256) |\n|---|---|---|---|---|---|\n")
		receipts.ReverseIterateByOffset(0, 10, func(_ string, v any) bool {
			r := v.(*receiptRecord)
			shortHash := r.commitment
			if len(shortHash) > 16 {
				shortHash = shortHash[:16] + "..."
			}
			b.WriteString(ufmt.Sprintf(
				"| [%d](:receipt/%d) | `%s` | [#%d](:service/%d) | `%d` | `%d ugnot` | `%s` |\n",
				r.id, r.id, sanitize.TableCell(r.rail), r.serviceID, r.serviceID, r.units, r.amountUgnot, sanitize.TableCell(shortHash),
			))
			return false
		})
		b.WriteString("\n")
	}

	b.WriteString("## Explorer Navigation\n\n")
	b.WriteString("- [All Services](:services)\n")
	b.WriteString("- [Settlement & Dispute Monitor](:disputes)\n")
	b.WriteString("- Machine JSON: `:json/summary`, `:json/services`, `:json/service/<id>`, `:json/pass/<id>`, `:json/receipt/<id>`\n")
	return b.String()
}

func renderServices() string {
	var b strings.Builder
	b.WriteString("# Tollgate — Service Directory\n\n")
	b.WriteString("[Back to Home](:)\n\n")
	if services.Size() == 0 {
		b.WriteString("_No services registered yet._\n")
		return b.String()
	}
	b.WriteString("| ID | Slug | Name | Endpoint | Unit Price | Challenge Window | Revenue |\n|---|---|---|---|---|---|---|\n")
	services.ReverseIterateByOffset(0, maxRenderRows, func(_ string, v any) bool {
		s := v.(*serviceRecord)
		b.WriteString(ufmt.Sprintf(
			"| [%d](:service/%d) | `%s` | %s | %s | `%d ugnot` | `%d blocks` | `%d ugnot` |\n",
			s.id, s.id,
			sanitize.TableCell(s.slug),
			sanitize.TableCell(s.name),
			sanitize.TableCell(s.endpointURL),
			s.unitPriceUgnot,
			s.challengeBlocks,
			s.totalRevenue,
		))
		return false
	})
	return b.String()
}

func renderServiceDetail(rawID string) string {
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		if v := slugIndex.Get(rawID); v != nil {
			id = v.(uint64)
		}
	}
	svc := getServiceRecord(id)
	if svc == nil {
		return "# Service Not Found\n\nNo service matches " + sanitize.InlineCode(rawID) + ".\n\n[Back to Home](:)\n"
	}

	memoExample, _ := ptollgate.FormatInvoiceMemo(svc.id, "req-0001")
	status := StatusActive
	if svc.paused {
		status = StatusPaused
	}

	var b strings.Builder
	b.WriteString("# Service #" + strconv.FormatUint(svc.id, 10) + ": " + sanitize.InlineText(svc.name) + "\n\n")
	b.WriteString("[Back to Home](:) | [All Services](:services)\n\n")
	if svc.description != "" {
		b.WriteString(sanitize.Block(svc.description))
	}
	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| Slug | " + sanitize.InlineCode(svc.slug) + " |\n")
	b.WriteString("| Status | `" + status + "` |\n")
	b.WriteString("| Owner | `" + svc.owner.String() + "` |\n")
	b.WriteString("| PayTo | `" + svc.payTo.String() + "` |\n")
	b.WriteString("| Endpoint | " + sanitize.TableCell(svc.endpointURL) + " |\n")
	b.WriteString(ufmt.Sprintf("| Unit Price | `%d ugnot` (volume discounts up to 20%%) |\n", svc.unitPriceUgnot))
	b.WriteString(ufmt.Sprintf("| Subscription | `%d ugnot` for `%d` calls / `%d` blocks |\n", svc.subPriceUgnot, svc.subQuota, svc.subBlocks))
	b.WriteString(ufmt.Sprintf("| Challenge Window | `%d` blocks |\n", svc.challengeBlocks))
	b.WriteString(ufmt.Sprintf("| Total Metered Calls | `%d` |\n", svc.totalCalls))
	b.WriteString(ufmt.Sprintf("| Total Revenue | `%d ugnot` |\n", svc.totalRevenue))
	b.WriteString("| x402 Invoice Memo | " + sanitize.InlineCode(memoExample) + " |\n")
	return b.String()
}

func renderPassDetail(rawID string) string {
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		return "# Pass Not Found\n\nInvalid pass ID " + sanitize.InlineCode(rawID) + ".\n"
	}
	p, ok := GetPass(id)
	if !ok {
		return "# Pass Not Found\n\nPass " + sanitize.InlineCode(rawID) + " does not exist.\n"
	}
	status := StatusActive
	if p.Closed {
		status = StatusClosed
	} else if runtime.ChainHeight() >= p.ExpiryHeight {
		status = "expired"
	}
	sessLabel := "master-key"
	if p.UsedSessionKey {
		sessLabel = "session-key (" + p.SessionKeyAddr.String() + ")"
	}
	var b strings.Builder
	b.WriteString(ufmt.Sprintf("# Credit Pass #%d\n\n[Back to Home](:) | [Service #%d](:service/%d)\n\n", p.ID, p.ServiceID, p.ServiceID))
	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| Status | `" + status + "` |\n")
	b.WriteString("| Buyer | `" + p.Buyer.String() + "` |\n")
	b.WriteString("| Signer Mode | `" + sanitize.TableCell(sessLabel) + "` |\n")
	b.WriteString(ufmt.Sprintf("| Deposited | `%d ugnot` |\n", p.DepositUgnot))
	b.WriteString(ufmt.Sprintf("| Spent | `%d ugnot` |\n", p.SpentUgnot))
	b.WriteString(ufmt.Sprintf("| Pending Escrow | `%d ugnot` |\n", p.PendingUgnot))
	b.WriteString(ufmt.Sprintf("| Available | `%d ugnot` |\n", p.AvailableUgnot))
	b.WriteString(ufmt.Sprintf("| Max Per Call | `%d ugnot` |\n", p.MaxPerCallUgnot))
	b.WriteString(ufmt.Sprintf("| Expiry Height | `%d` (current: `%d`) |\n", p.ExpiryHeight, runtime.ChainHeight()))
	b.WriteString(ufmt.Sprintf("| Settled Calls | `%d` |\n", p.SettledCalls))
	return b.String()
}

func renderSubDetail(rawID string) string {
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		return "# Subscription Not Found\n"
	}
	s, ok := GetSubscription(id)
	if !ok {
		return "# Subscription Not Found\n"
	}
	state := "expired"
	if s.Active {
		state = StatusActive
	}
	var b strings.Builder
	b.WriteString(ufmt.Sprintf("# Subscription #%d\n\n[Back to Home](:) | [Service #%d](:service/%d)\n\n", s.ID, s.ServiceID, s.ServiceID))
	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| Status | `" + state + "` |\n")
	b.WriteString("| Buyer | `" + s.Buyer.String() + "` |\n")
	b.WriteString(ufmt.Sprintf("| Paid | `%d ugnot` |\n", s.PaidUgnot))
	b.WriteString(ufmt.Sprintf("| Quota | `%d / %d` (`%d` remaining) |\n", s.QuotaUsed, s.QuotaTotal, s.QuotaRemaining))
	b.WriteString(ufmt.Sprintf("| Window | block `%d` -> `%d` (current: `%d`) |\n", s.StartHeight, s.ExpiryHeight, runtime.ChainHeight()))
	return b.String()
}

func renderReceiptDetail(rawID string) string {
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		if rid, ok := VerifyReceiptByCommitment(rawID); ok {
			id = rid
		} else {
			return "# Receipt Not Found\n\nUnknown receipt " + sanitize.InlineCode(rawID) + ".\n"
		}
	}
	r, ok := GetReceipt(id)
	if !ok {
		return "# Receipt Not Found\n"
	}
	var b strings.Builder
	b.WriteString(ufmt.Sprintf("# Verifiable Receipt #%d\n\n[Back to Home](:) | [Service #%d](:service/%d)\n\n", r.ID, r.ServiceID, r.ServiceID))
	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| Payment Rail | `" + sanitize.TableCell(r.Rail) + "` |\n")
	b.WriteString(ufmt.Sprintf("| Service ID | `%d` |\n", r.ServiceID))
	b.WriteString(ufmt.Sprintf("| Pass ID | `%d` |\n", r.PassID))
	b.WriteString(ufmt.Sprintf("| Subscription ID | `%d` |\n", r.SubID))
	b.WriteString("| Buyer | `" + r.Buyer.String() + "` |\n")
	b.WriteString("| Provider | `" + r.Provider.String() + "` |\n")
	b.WriteString(ufmt.Sprintf("| Units | `%d` |\n", r.Units))
	b.WriteString(ufmt.Sprintf("| Amount | `%d ugnot` |\n", r.AmountUgnot))
	b.WriteString("| Nonce | " + sanitize.InlineCode(r.Nonce) + " |\n")
	b.WriteString("| Resource Hash | `" + sanitize.TableCell(r.ResourceHash) + "` |\n")
	b.WriteString("| SHA-256 Commitment | `" + sanitize.TableCell(r.Commitment) + "` |\n")
	if r.TxHash != "" {
		b.WriteString("| x402 Tx Hash | " + sanitize.InlineCode(r.TxHash) + " |\n")
	}
	b.WriteString(ufmt.Sprintf("| Anchored Height | `%d` |\n", r.Height))
	return b.String()
}

func renderDisputes() string {
	var b strings.Builder
	b.WriteString("# Tollgate — Settlements & Challenge Window Monitor\n\n[Back to Home](:)\n\n")
	if settlements.Size() == 0 {
		b.WriteString("_No metered settlements submitted yet._\n")
		return b.String()
	}
	b.WriteString("| ID | Pass | Units | Net | Discount | Challenge End | Status | Reason |\n|---|---|---|---|---|---|---|---|\n")
	settlements.ReverseIterateByOffset(0, maxRenderRows, func(_ string, v any) bool {
		st := v.(*settlementRecord)
		reason := "—"
		if st.disputeReason != "" {
			reason = sanitize.TableCell(st.disputeReason)
		}
		b.WriteString(ufmt.Sprintf(
			"| `%d` | [#%d](:pass/%d) | `%d` | `%d ugnot` | `%d bps` | `%d` | `%s` | %s |\n",
			st.id, st.passID, st.passID, st.units, st.netUgnot, st.discountBps, st.challengeEnd, st.status, reason,
		))
		return false
	})
	return b.String()
}

func renderJSON(parts []string) string {
	if len(parts) == 0 || parts[0] == "summary" {
		return ufmt.Sprintf(
			`{"protocol":%s,"chainId":%s,"height":%d,"vaultBalanceUgnot":%d,"services":%d,"passes":%d,"subscriptions":%d,"settlements":%d,"receipts":%d,"totalDepositedUgnot":%d,"totalSettledUgnot":%d,"totalX402AnchoredUgnot":%d,"totalSubRevenueUgnot":%d,"totalRefundedUgnot":%d}`,
			jsonEscape(ptollgate.ProtocolVersion),
			jsonEscape(runtime.ChainID()),
			runtime.ChainHeight(),
			VaultBalanceUgnot(),
			services.Size(),
			passes.Size(),
			subs.Size(),
			settlements.Size(),
			receipts.Size(),
			totalDepositedUgnot,
			totalSettledUgnot,
			totalX402AnchoredUgnot,
			totalSubRevenueUgnot,
			totalRefundedUgnot,
		)
	}
	if parts[0] == "services" {
		var b strings.Builder
		b.WriteByte('[')
		first := true
		services.ReverseIterateByOffset(0, maxRenderRows, func(_ string, v any) bool {
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString(serviceJSON(v.(*serviceRecord)))
			return false
		})
		b.WriteByte(']')
		return b.String()
	}
	if len(parts) == 2 {
		id, err := strconv.ParseUint(parts[1], 10, 64)
		switch parts[0] {
		case "service":
			if err != nil {
				if v := slugIndex.Get(parts[1]); v != nil {
					id = v.(uint64)
				}
			}
			if s := getServiceRecord(id); s != nil {
				return serviceJSON(s)
			}
		case "pass":
			if err == nil {
				if p, ok := GetPass(id); ok {
					return ufmt.Sprintf(
						`{"id":%d,"serviceId":%d,"buyer":%s,"depositUgnot":%d,"spentUgnot":%d,"pendingUgnot":%d,"availableUgnot":%d,"maxPerCallUgnot":%d,"expiryHeight":%d,"openedHeight":%d,"closed":%t,"usedSessionKey":%t,"settledCalls":%d,"currentHeight":%d}`,
						p.ID, p.ServiceID, jsonEscape(p.Buyer.String()), p.DepositUgnot, p.SpentUgnot, p.PendingUgnot,
						p.AvailableUgnot, p.MaxPerCallUgnot, p.ExpiryHeight, p.OpenedHeight, p.Closed,
						p.UsedSessionKey, p.SettledCalls, runtime.ChainHeight(),
					)
				}
			}
		case "sub":
			if err == nil {
				if s, ok := GetSubscription(id); ok {
					return ufmt.Sprintf(
						`{"id":%d,"serviceId":%d,"buyer":%s,"paidUgnot":%d,"quotaTotal":%d,"quotaUsed":%d,"quotaRemaining":%d,"startHeight":%d,"expiryHeight":%d,"active":%t,"currentHeight":%d}`,
						s.ID, s.ServiceID, jsonEscape(s.Buyer.String()), s.PaidUgnot, s.QuotaTotal,
						s.QuotaUsed, s.QuotaRemaining, s.StartHeight, s.ExpiryHeight, s.Active, runtime.ChainHeight(),
					)
				}
			}
		case "receipt":
			if err != nil {
				if rid, ok := VerifyReceiptByCommitment(parts[1]); ok {
					id = rid
				}
			}
			if r, ok := GetReceipt(id); ok {
				return ufmt.Sprintf(
					`{"id":%d,"rail":%s,"serviceId":%d,"passId":%d,"subId":%d,"buyer":%s,"provider":%s,"units":%d,"amountUgnot":%d,"nonce":%s,"resourceHash":%s,"commitment":%s,"txHash":%s,"height":%d}`,
					r.ID, jsonEscape(r.Rail), r.ServiceID, r.PassID, r.SubID, jsonEscape(r.Buyer.String()),
					jsonEscape(r.Provider.String()), r.Units, r.AmountUgnot, jsonEscape(r.Nonce),
					jsonEscape(r.ResourceHash), jsonEscape(r.Commitment), jsonEscape(r.TxHash), r.Height,
				)
			}
		case "settlement":
			if err == nil {
				if st, ok := GetSettlement(id); ok {
					return ufmt.Sprintf(
						`{"id":%d,"serviceId":%d,"passId":%d,"buyer":%s,"provider":%s,"units":%d,"grossUgnot":%d,"discountBps":%d,"netUgnot":%d,"nonce":%s,"resourceHash":%s,"commitment":%s,"claimedHeight":%d,"challengeEnd":%d,"status":%s,"disputeReason":%s,"receiptId":%d}`,
						st.ID, st.ServiceID, st.PassID, jsonEscape(st.Buyer.String()), jsonEscape(st.Provider.String()),
						st.Units, st.GrossUgnot, st.DiscountBps, st.NetUgnot, jsonEscape(st.Nonce),
						jsonEscape(st.ResourceHash), jsonEscape(st.Commitment), st.ClaimedHeight,
						st.ChallengeEnd, jsonEscape(st.Status), jsonEscape(st.DisputeReason), st.ReceiptID,
					)
				}
			}
		}
	}
	return `{"error":"not_found"}`
}

func serviceJSON(s *serviceRecord) string {
	return ufmt.Sprintf(
		`{"id":%d,"slug":%s,"name":%s,"description":%s,"endpointUrl":%s,"owner":%s,"payTo":%s,"unitPriceUgnot":%d,"subPriceUgnot":%d,"subBlocks":%d,"subQuota":%d,"challengeBlocks":%d,"paused":%t,"createdHeight":%d,"totalCalls":%d,"totalRevenue":%d}`,
		s.id, jsonEscape(s.slug), jsonEscape(s.name), jsonEscape(s.description), jsonEscape(s.endpointURL),
		jsonEscape(s.owner.String()), jsonEscape(s.payTo.String()), s.unitPriceUgnot, s.subPriceUgnot,
		s.subBlocks, s.subQuota, s.challengeBlocks, s.paused, s.createdHeight, s.totalCalls, s.totalRevenue,
	)
}
