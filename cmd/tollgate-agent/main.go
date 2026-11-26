// Command tollgate-agent is an autonomous AI payment client and MCP tool bridge
// that negotiates HTTP 402 (x402 v2) + Tollgate prepaid passes & subscriptions,
// persists local PayWord SHA-256 hash-chain anchors across back-to-back calls
// before on-chain settlement, verifies SHA-256 receipt commitments locally and
// on-chain, and prints audit proofs.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tollgate "github.com/mevlanayalcin/tollgate/sdk/go"
)

type agentAnchorState struct {
	PassAnchors map[string]string `json:"passAnchors"`
	SubAnchors  map[string]string `json:"subAnchors"`
}

func loadAgentState(path string) agentAnchorState {
	st := agentAnchorState{
		PassAnchors: make(map[string]string),
		SubAnchors:  make(map[string]string),
	}
	if path == "" {
		return st
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	if st.PassAnchors == nil {
		st.PassAnchors = make(map[string]string)
	}
	if st.SubAnchors == nil {
		st.SubAnchors = make(map[string]string)
	}
	return st
}

func saveAgentState(path string, st agentAnchorState) {
	if path == "" {
		return
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "keygen":
		runKeygen(os.Args[2:])
	case "payword-proof":
		runPayWordProof(os.Args[2:])
	case "call":
		runCall(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: tollgate-agent <keygen|payword-proof|call|verify|mcp> [flags]")
}

func runKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	chainID := fs.String("chain-id", "onyx-1", "Gno chain ID for domain separation")
	depth := fs.Int("depth", 1000, "PayWord hash-chain depth (max total authorized units)")
	_ = fs.Parse(args)

	seed, err := tollgate.NewPayWordSeed()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keygen failed: %v\n", err)
		os.Exit(1)
	}
	ch, err := tollgate.BuildPayWordChain(*chainID, seed, *depth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build chain failed: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"chainId":   *chainID,
		"depth":     *depth,
		"seedHex":   seed,
		"anchorHex": ch.AnchorHex(),
	})
}

func runPayWordProof(args []string) {
	fs := flag.NewFlagSet("payword-proof", flag.ExitOnError)
	seed := fs.String("seed", "", "64-char PayWord seed hex")
	chainID := fs.String("chain-id", "onyx-1", "Gno chain ID")
	depth := fs.Int("depth", 1000, "PayWord chain depth")
	anchor := fs.String("anchor", "", "Current anchor hex (defaults to initial w_depth)")
	units := fs.Int64("units", 1, "Metered units to authorize")
	_ = fs.Parse(args)

	if *seed == "" {
		fmt.Fprintln(os.Stderr, "--seed is required")
		os.Exit(1)
	}
	ch, err := tollgate.BuildPayWordChain(*chainID, strings.TrimSpace(*seed), *depth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	curAnchor := strings.TrimSpace(*anchor)
	if curAnchor == "" {
		curAnchor = ch.AnchorHex()
	}
	proof, _, err := ch.ProofForAnchor(curAnchor, *units)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(proof)
}

func runCall(args []string) {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	targetURL := fs.String("url", "", "Target HTTP 402 API URL")
	rpcURL := fs.String("rpc", "https://rpc.onyx.testnets.gno.land:443", "Gno RPC URL")
	realmPath := fs.String("realm", "", "Tollgate realm path")
	passID := fs.Uint64("pass", 0, "Tollgate prepaid credit pass ID")
	subID := fs.Uint64("sub", 0, "Tollgate subscription ID")
	units := fs.Int64("units", 1, "Metered units to request")
	nonce := fs.String("nonce", fmt.Sprintf("agent-%d", time.Now().UnixNano()), "Unique request nonce")
	voucherKey := fs.String("voucher-key", "", "Buyer 64-char PayWord seed hex")
	paywordDepth := fs.Int("payword-depth", 1000, "PayWord hash-chain depth")
	stateFile := fs.String("state-file", "", "Optional JSON file persisting local PayWord anchor across calls before on-chain flush")
	syncChain := fs.Bool("sync-chain", false, "Force re-sync PayWord anchor from on-chain realm instead of local state file")
	_ = fs.Parse(args)

	if *targetURL == "" {
		fmt.Fprintln(os.Stderr, "--url is required")
		os.Exit(1)
	}

	out, err := executeAgentCall(context.Background(), *targetURL, *rpcURL, *realmPath, *passID, *subID, *units, *nonce, *voucherKey, *paywordDepth, *stateFile, *syncChain)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

type AgentCallReport struct {
	URL                string                    `json:"url"`
	ProbeStatus        int                       `json:"probeStatus"`
	DiscoveredTerms    *tollgate.PaymentRequired `json:"discoveredTerms,omitempty"`
	SelectedRail       string                    `json:"selectedRail"`
	FinalStatus        int                       `json:"finalStatus"`
	NetUgnot           int64                     `json:"netUgnot"`
	Nonce              string                    `json:"nonce"`
	ResourceHash       string                    `json:"resourceHash"`
	VoucherProofHex    string                    `json:"voucherProofHex,omitempty"`
	ReceiptCommitment  string                    `json:"receiptCommitment"`
	LocalProofVerified bool                      `json:"localProofVerified"`
	ResponseBody       json.RawMessage           `json:"responseBody,omitempty"`
}

func executeAgentCall(
	ctx context.Context,
	targetURL, rpcURL, realmPath string,
	passID, subID uint64,
	units int64,
	nonce, seedHex string,
	depth int,
	stateFile string,
	syncChain bool,
) (AgentCallReport, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	report := AgentCallReport{URL: targetURL, Nonce: nonce}

	probeResp, err := client.Get(targetURL)
	if err != nil {
		return report, fmt.Errorf("probe failed: %w", err)
	}
	defer probeResp.Body.Close()
	report.ProbeStatus = probeResp.StatusCode

	if probeResp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(probeResp.Body)
		report.FinalStatus = probeResp.StatusCode
		report.SelectedRail = "free"
		report.ResponseBody = json.RawMessage(body)
		return report, nil
	}

	pr, err := tollgate.DecodePaymentRequired(probeResp.Header.Get(tollgate.HeaderPaymentRequired))
	if err != nil {
		return report, fmt.Errorf("decode 402 PAYMENT-REQUIRED: %w", err)
	}
	report.DiscoveredTerms = &pr
	if realmPath == "" {
		realmPath = probeResp.Header.Get(tollgate.HeaderTollgateRealm)
	}
	svcID, _ := strconv.ParseUint(probeResp.Header.Get(tollgate.HeaderTollgateServiceID), 10, 64)

	var buyerAddr, chainAnchor string
	var usedPassID uint64
	chainID := "onyx-1"
	payTo := ""
	if len(pr.Accepts) > 0 {
		chainID = strings.TrimPrefix(pr.Accepts[0].Network, "gno:")
		payTo = pr.Accepts[0].PayTo
	}

	chainClient := tollgate.NewRPCClient(rpcURL, realmPath)
	if subID > 0 {
		sub, err := chainClient.Subscription(ctx, subID)
		if err != nil {
			return report, fmt.Errorf("query sub %d: %w", subID, err)
		}
		if sub.Active && sub.QuotaRemaining >= units {
			report.SelectedRail = "subscription"
			buyerAddr = sub.Buyer
			chainAnchor = sub.VoucherPubKey
		}
	}
	if report.SelectedRail == "" && passID > 0 {
		pass, err := chainClient.Pass(ctx, passID)
		if err != nil {
			return report, fmt.Errorf("query pass %d: %w", passID, err)
		}
		report.SelectedRail = "metered-pass"
		buyerAddr = pass.Buyer
		usedPassID = passID
		chainAnchor = pass.VoucherPubKey
	}
	if report.SelectedRail == "" {
		return report, fmt.Errorf("endpoint requires payment (402): supply --pass <id> or --sub <id>")
	}

	st := loadAgentState(stateFile)
	stateKey := fmt.Sprintf("%s|%s|%d", chainID, realmPath, usedPassID)
	if report.SelectedRail == "subscription" {
		stateKey = fmt.Sprintf("%s|%s|%d", chainID, realmPath, subID)
	}

	activeAnchor := chainAnchor
	usedLocalAnchor := false
	if !syncChain && stateFile != "" {
		if report.SelectedRail == "subscription" && st.SubAnchors[stateKey] != "" {
			activeAnchor = st.SubAnchors[stateKey]
			usedLocalAnchor = true
		} else if report.SelectedRail == "metered-pass" && st.PassAnchors[stateKey] != "" {
			activeAnchor = st.PassAnchors[stateKey]
			usedLocalAnchor = true
		}
	}

	parsedURL, _ := url.Parse(targetURL)
	expectedResHash := tollgate.HashResource("GET " + parsedURL.RequestURI())

	var pwChain *tollgate.PayWordChain
	if strings.TrimSpace(seedHex) != "" && activeAnchor != "" {
		if depth <= 0 {
			depth = 1000
		}
		pwChain, err = tollgate.BuildPayWordChain(chainID, strings.TrimSpace(seedHex), depth)
		if err != nil {
			return report, err
		}
	}

	doRequest := func(anchor string) (*http.Response, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, nil, "", err
		}
		req.Header.Set(tollgate.HeaderTollgateUnits, strconv.FormatInt(units, 10))
		req.Header.Set(tollgate.HeaderTollgateNonce, nonce)
		if report.SelectedRail == "subscription" {
			req.Header.Set(tollgate.HeaderTollgateSub, strconv.FormatUint(subID, 10))
		} else {
			req.Header.Set(tollgate.HeaderTollgatePass, strconv.FormatUint(usedPassID, 10))
		}
		var proofHex string
		if pwChain != nil && anchor != "" {
			proofHex, _, err = pwChain.ProofForAnchor(anchor, units)
			if err != nil {
				return nil, nil, "", err
			}
			req.Header.Set(tollgate.HeaderTollgateVoucherSig, proofHex)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, proofHex, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body, proofHex, nil
	}

	resp, body, proofHex, err := doRequest(activeAnchor)
	if err != nil {
		return report, err
	}
	// If local cached anchor drifted from chain (e.g. another client settled), re-sync once from chain
	if resp.StatusCode == http.StatusUnauthorized && usedLocalAnchor && chainAnchor != "" && chainAnchor != activeAnchor {
		resp, body, proofHex, err = doRequest(chainAnchor)
		if err != nil {
			return report, err
		}
	}

	report.FinalStatus = resp.StatusCode
	report.VoucherProofHex = proofHex
	if json.Valid(body) {
		report.ResponseBody = json.RawMessage(body)
	}
	report.ResourceHash = resp.Header.Get(tollgate.HeaderTollgateResHash)
	report.ReceiptCommitment = resp.Header.Get(tollgate.HeaderTollgateCommitment)
	report.NetUgnot, _ = strconv.ParseInt(resp.Header.Get(tollgate.HeaderTollgateNetUgnot), 10, 64)

	// Advance persisted local anchor ONLY on HTTP 200 OK
	if resp.StatusCode == http.StatusOK && proofHex != "" && stateFile != "" {
		if report.SelectedRail == "subscription" {
			st.SubAnchors[stateKey] = proofHex
		} else {
			st.PassAnchors[stateKey] = proofHex
		}
		saveAgentState(stateFile, st)
	}

	report.LocalProofVerified = resp.StatusCode == http.StatusOK &&
		report.ResourceHash == expectedResHash &&
		tollgate.VerifyReceiptCommitment(
			report.ReceiptCommitment,
			chainID, svcID, usedPassID, buyerAddr, payTo,
			units, report.NetUgnot, nonce, expectedResHash,
		)
	return report, nil
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	rpcURL := fs.String("rpc", "https://rpc.onyx.testnets.gno.land:443", "Gno RPC URL")
	realmPath := fs.String("realm", "", "Tollgate realm path")
	receipt := fs.String("receipt", "", "Receipt ID or SHA-256 commitment hex")
	_ = fs.Parse(args)
	if *realmPath == "" || *receipt == "" {
		fmt.Fprintln(os.Stderr, "--realm and --receipt are required")
		os.Exit(1)
	}
	c := tollgate.NewRPCClient(*rpcURL, *realmPath)
	rec, err := c.Receipt(context.Background(), *receipt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "not found: %v\n", err)
		os.Exit(1)
	}
	sum, _ := c.Summary(context.Background())
	recomputed := tollgate.ReceiptCommitment(
		sum.ChainID, rec.ServiceID, rec.PassID, rec.Buyer, rec.Provider,
		rec.Units, rec.AmountUgnot, rec.Nonce, rec.ResourceHash,
	)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"onChainReceipt": rec,
		"recomputed":     recomputed,
		"verified":       recomputed == rec.Commitment && rec.SettlementStatus != "refunded",
	})
}

func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	rpcURL := fs.String("rpc", "https://rpc.onyx.testnets.gno.land:443", "Gno RPC URL")
	realmPath := fs.String("realm", "", "Tollgate realm path")
	defaultSeed := fs.String("voucher-key", "", "Default PayWord 64-char seed hex")
	defaultDepth := fs.Int("payword-depth", 1000, "Default PayWord chain depth")
	stateFile := fs.String("state-file", "", "Optional JSON file persisting local PayWord anchor across MCP calls")
	_ = fs.Parse(args)

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "tollgate-mcp", "version": "1.0.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "tollgate_paid_fetch",
						"description": "Fetch an HTTP 402 (x402 + Tollgate) API endpoint using a PayWord SHA-256 hash-chain pass or subscription and verify the cryptographic receipt.",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"url":        map[string]any{"type": "string"},
								"passId":     map[string]any{"type": "integer"},
								"subId":      map[string]any{"type": "integer"},
								"units":      map[string]any{"type": "integer"},
								"voucherKey": map[string]any{"type": "string"},
							},
							"required": []string{"url"},
						},
					},
					{
						"name":        "tollgate_chain_summary",
						"description": "Read live Tollgate realm telemetry (TVL, services, passes, receipts) from Gno.land.",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					},
				},
			}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "tollgate_chain_summary" {
				sum, err := tollgate.NewRPCClient(*rpcURL, *realmPath).Summary(context.Background())
				text, _ := json.Marshal(sum)
				if err != nil {
					text = []byte(err.Error())
				}
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
			} else if p.Name == "tollgate_paid_fetch" {
				u, _ := p.Arguments["url"].(string)
				pidF, _ := p.Arguments["passId"].(float64)
				sidF, _ := p.Arguments["subId"].(float64)
				unitsF, _ := p.Arguments["units"].(float64)
				vKey, _ := p.Arguments["voucherKey"].(string)
				if vKey == "" {
					vKey = *defaultSeed
				}
				if unitsF < 1 {
					unitsF = 1
				}
				rep, err := executeAgentCall(context.Background(), u, *rpcURL, *realmPath, uint64(pidF), uint64(sidF), int64(unitsF), fmt.Sprintf("mcp-%d", time.Now().UnixNano()), vKey, *defaultDepth, *stateFile, false)
				text, _ := json.Marshal(rep)
				if err != nil {
					text = []byte(err.Error())
				}
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
			}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		raw, _ := json.Marshal(resp)
		fmt.Println(string(raw))
	}
}
