// Command tollgate-agent is an autonomous AI payment client and MCP tool bridge
// that negotiates HTTP 402 (x402 v2) + Tollgate prepaid passes & subscriptions,
// verifies SHA-256 receipt commitments locally and on-chain, and prints audit proofs.
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
	"strconv"
	"strings"
	"time"

	tollgate "github.com/mevlanayalcin/tollgate/sdk/go"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
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
	fmt.Fprintln(os.Stderr, "Usage: tollgate-agent <call|verify|mcp> [flags]")
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
	_ = fs.Parse(args)

	if *targetURL == "" {
		fmt.Fprintln(os.Stderr, "--url is required")
		os.Exit(1)
	}

	out, err := executeAgentCall(context.Background(), *targetURL, *rpcURL, *realmPath, *passID, *subID, *units, *nonce)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

type AgentCallReport struct {
	URL                string                   `json:"url"`
	ProbeStatus        int                      `json:"probeStatus"`
	DiscoveredTerms    *tollgate.PaymentRequired `json:"discoveredTerms,omitempty"`
	SelectedRail       string                   `json:"selectedRail"`
	FinalStatus        int                      `json:"finalStatus"`
	NetUgnot           int64                    `json:"netUgnot"`
	Nonce              string                   `json:"nonce"`
	ResourceHash       string                   `json:"resourceHash"`
	ReceiptCommitment  string                   `json:"receiptCommitment"`
	LocalProofVerified bool                     `json:"localProofVerified"`
	ResponseBody       json.RawMessage          `json:"responseBody,omitempty"`
}

func executeAgentCall(
	ctx context.Context,
	targetURL, rpcURL, realmPath string,
	passID, subID uint64,
	units int64,
	nonce string,
) (AgentCallReport, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	report := AgentCallReport{URL: targetURL, Nonce: nonce}

	// Step 1: Unpaid probe to discover x402 v2 terms + Tollgate realm/service ID
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

	// Step 2: Select optimal rail (Subscription -> Prepaid Pass)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return report, err
	}
	req.Header.Set(tollgate.HeaderTollgateUnits, strconv.FormatInt(units, 10))
	req.Header.Set(tollgate.HeaderTollgateNonce, nonce)

	var buyerAddr string
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
			req.Header.Set(tollgate.HeaderTollgateSub, strconv.FormatUint(subID, 10))
			buyerAddr = sub.Buyer
		}
	}
	if report.SelectedRail == "" && passID > 0 {
		pass, err := chainClient.Pass(ctx, passID)
		if err != nil {
			return report, fmt.Errorf("query pass %d: %w", passID, err)
		}
		report.SelectedRail = "metered-pass"
		req.Header.Set(tollgate.HeaderTollgatePass, strconv.FormatUint(passID, 10))
		buyerAddr = pass.Buyer
		usedPassID = passID
	}
	if report.SelectedRail == "" {
		return report, fmt.Errorf("endpoint requires payment (402): supply --pass <id> or --sub <id>")
	}

	// Step 3: Execute paid request & verify deterministic receipt commitment
	resp, err := client.Do(req)
	if err != nil {
		return report, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	report.FinalStatus = resp.StatusCode
	if json.Valid(body) {
		report.ResponseBody = json.RawMessage(body)
	}
	report.ResourceHash = resp.Header.Get(tollgate.HeaderTollgateResHash)
	report.ReceiptCommitment = resp.Header.Get(tollgate.HeaderTollgateCommitment)
	report.NetUgnot, _ = strconv.ParseInt(resp.Header.Get(tollgate.HeaderTollgateNetUgnot), 10, 64)

	parsedURL, _ := url.Parse(targetURL)
	expectedResHash := tollgate.HashResource("GET " + parsedURL.RequestURI())
	report.LocalProofVerified = report.ResourceHash == expectedResHash &&
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
		"verified":       recomputed == rec.Commitment,
	})
}

// runMCP exposes Tollgate as an MCP (Model Context Protocol) JSON-RPC 2.0 stdio server
// compatible with gnomcp and autonomous AI agents.
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	rpcURL := fs.String("rpc", "https://rpc.onyx.testnets.gno.land:443", "Gno RPC URL")
	realmPath := fs.String("realm", "", "Tollgate realm path")
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
						"description": "Fetch an HTTP 402 (x402 + Tollgate) API endpoint using an on-chain pass or subscription and verify the cryptographic receipt.",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"url":    map[string]any{"type": "string"},
								"passId": map[string]any{"type": "integer"},
								"subId":  map[string]any{"type": "integer"},
								"units":  map[string]any{"type": "integer"},
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
				if unitsF < 1 {
					unitsF = 1
				}
				rep, err := executeAgentCall(context.Background(), u, *rpcURL, *realmPath, uint64(pidF), uint64(sidF), int64(unitsF), fmt.Sprintf("mcp-%d", time.Now().UnixNano()))
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
