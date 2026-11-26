package tollgate

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ChainSummary struct {
	Protocol             string `json:"protocol"`
	ChainID              string `json:"chainId"`
	Height               int64  `json:"height"`
	VaultBalanceUgnot    int64  `json:"vaultBalanceUgnot"`
	Services             int    `json:"services"`
	Passes               int    `json:"passes"`
	Subscriptions        int    `json:"subscriptions"`
	Settlements          int    `json:"settlements"`
	Receipts             int    `json:"receipts"`
	TotalDepositedUgnot  int64  `json:"totalDepositedUgnot"`
	TotalSettledUgnot    int64  `json:"totalSettledUgnot"`
	TotalX402Anchored    int64  `json:"totalX402AnchoredUgnot"`
	TotalSubRevenueUgnot int64  `json:"totalSubRevenueUgnot"`
	TotalRefundedUgnot   int64  `json:"totalRefundedUgnot"`
}

type ServiceInfo struct {
	ID              uint64 `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	EndpointURL     string `json:"endpointUrl"`
	Owner           string `json:"owner"`
	PayTo           string `json:"payTo"`
	UnitPriceUgnot  int64  `json:"unitPriceUgnot"`
	SubPriceUgnot   int64  `json:"subPriceUgnot"`
	SubBlocks       int64  `json:"subBlocks"`
	SubQuota        int64  `json:"subQuota"`
	ChallengeBlocks int64  `json:"challengeBlocks"`
	Paused          bool   `json:"paused"`
	CreatedHeight   int64  `json:"createdHeight"`
	TotalCalls      int64  `json:"totalCalls"`
	EscrowedRevenue     int64  `json:"escrowedRevenue"`
	X402AttestedRevenue int64  `json:"x402AttestedRevenue"`
}

type PassInfo struct {
	ID              uint64 `json:"id"`
	ServiceID       uint64 `json:"serviceId"`
	Buyer           string `json:"buyer"`
	DepositUgnot    int64  `json:"depositUgnot"`
	SpentUgnot      int64  `json:"spentUgnot"`
	PendingUgnot    int64  `json:"pendingUgnot"`
	AvailableUgnot  int64  `json:"availableUgnot"`
	MaxPerCallUgnot int64  `json:"maxPerCallUgnot"`
	ExpiryHeight    int64  `json:"expiryHeight"`
	OpenedHeight    int64  `json:"openedHeight"`
	Closed          bool   `json:"closed"`
	UsedSessionKey  bool   `json:"usedSessionKey"`
	VoucherPubKey   string `json:"voucherPubKey"`
	SettledCalls    int64  `json:"settledCalls"`
	CurrentHeight   int64  `json:"currentHeight"`
}

type SubscriptionInfo struct {
	ID             uint64 `json:"id"`
	ServiceID      uint64 `json:"serviceId"`
	Buyer          string `json:"buyer"`
	PaidUgnot      int64  `json:"paidUgnot"`
	QuotaTotal     int64  `json:"quotaTotal"`
	QuotaUsed      int64  `json:"quotaUsed"`
	QuotaRemaining int64  `json:"quotaRemaining"`
	StartHeight    int64  `json:"startHeight"`
	ExpiryHeight   int64  `json:"expiryHeight"`
	Active         bool   `json:"active"`
	VoucherPubKey  string `json:"voucherPubKey"`
	CurrentHeight  int64  `json:"currentHeight"`
}

type ReceiptInfo struct {
	ID           uint64 `json:"id"`
	Rail         string `json:"rail"`
	ServiceID    uint64 `json:"serviceId"`
	PassID       uint64 `json:"passId"`
	SubID            uint64 `json:"subId"`
	SettlementID     uint64 `json:"settlementId"`
	SettlementStatus string `json:"settlementStatus"`
	BuyerSigned      bool   `json:"buyerSigned"`
	Buyer        string `json:"buyer"`
	Provider     string `json:"provider"`
	Units        int64  `json:"units"`
	AmountUgnot  int64  `json:"amountUgnot"`
	Nonce        string `json:"nonce"`
	ResourceHash string `json:"resourceHash"`
	Commitment   string `json:"commitment"`
	TxHash       string `json:"txHash"`
	Height       int64  `json:"height"`
}

type ChainReader interface {
	Summary(ctx context.Context) (ChainSummary, error)
	Services(ctx context.Context) ([]ServiceInfo, error)
	Service(ctx context.Context, idOrSlug string) (ServiceInfo, error)
	Pass(ctx context.Context, passID uint64) (PassInfo, error)
	Subscription(ctx context.Context, subID uint64) (SubscriptionInfo, error)
	Receipt(ctx context.Context, idOrHash string) (ReceiptInfo, error)
	CheckPassQuota(ctx context.Context, passID uint64, units int64) (bool, error)
	CheckSubscriptionActive(ctx context.Context, subID uint64, units int64) (bool, error)
}

type RPCClient struct {
	RPCURL    string
	RealmPath string
	HTTP      *http.Client
}

func NewRPCClient(rpcURL, realmPath string) *RPCClient {
	return &RPCClient{
		RPCURL:    strings.TrimRight(rpcURL, "/"),
		RealmPath: strings.Trim(realmPath, "/"),
		HTTP:      &http.Client{Timeout: 15 * time.Second},
	}
}

type abciQueryEnvelope struct {
	Result struct {
		Response struct {
			ResponseBase struct {
				Error any    `json:"Error"`
				Data  string `json:"Data"`
				Log   string `json:"Log"`
			} `json:"ResponseBase"`
		} `json:"response"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

func (c *RPCClient) abciQuery(ctx context.Context, path, data string) (string, error) {
	u := fmt.Sprintf(
		"%s/abci_query?path=%s&data=0x%s",
		c.RPCURL,
		url.QueryEscape(`"`+path+`"`),
		hex.EncodeToString([]byte(data)),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("rpc %s status %d: %s", path, resp.StatusCode, string(body))
	}
	var env abciQueryEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("rpc decode %s: %w", path, err)
	}
	if env.Error != nil {
		return "", fmt.Errorf("rpc error %d: %s (%s)", env.Error.Code, env.Error.Message, env.Error.Data)
	}
	rb := env.Result.Response.ResponseBase
	if rb.Error != nil {
		return "", fmt.Errorf("abci error: %v (%s)", rb.Error, rb.Log)
	}
	if rb.Data == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(rb.Data)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	return string(raw), nil
}

func (c *RPCClient) QueryRender(ctx context.Context, subPath string) (string, error) {
	return c.abciQuery(ctx, "vm/qrender", c.RealmPath+":"+subPath)
}

func (c *RPCClient) QueryEval(ctx context.Context, expr string) (string, error) {
	return c.abciQuery(ctx, "vm/qeval", c.RealmPath+"."+expr)
}

func (c *RPCClient) Summary(ctx context.Context) (ChainSummary, error) {
	raw, err := c.QueryRender(ctx, "json/summary")
	if err != nil {
		return ChainSummary{}, err
	}
	var out ChainSummary
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ChainSummary{}, fmt.Errorf("parse summary json (%q): %w", raw, err)
	}
	return out, nil
}

func (c *RPCClient) Services(ctx context.Context) ([]ServiceInfo, error) {
	raw, err := c.QueryRender(ctx, "json/services")
	if err != nil {
		return nil, err
	}
	var out []ServiceInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse services json: %w", err)
	}
	return out, nil
}

func (c *RPCClient) Service(ctx context.Context, idOrSlug string) (ServiceInfo, error) {
	raw, err := c.QueryRender(ctx, "json/service/"+idOrSlug)
	if err != nil {
		return ServiceInfo{}, err
	}
	if strings.Contains(raw, `"error":"not_found"`) {
		return ServiceInfo{}, fmt.Errorf("service %q not found", idOrSlug)
	}
	var out ServiceInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ServiceInfo{}, err
	}
	return out, nil
}

func (c *RPCClient) Pass(ctx context.Context, passID uint64) (PassInfo, error) {
	raw, err := c.QueryRender(ctx, "json/pass/"+strconv.FormatUint(passID, 10))
	if err != nil {
		return PassInfo{}, err
	}
	if strings.Contains(raw, `"error":"not_found"`) {
		return PassInfo{}, fmt.Errorf("pass %d not found", passID)
	}
	var out PassInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return PassInfo{}, err
	}
	return out, nil
}

func (c *RPCClient) Subscription(ctx context.Context, subID uint64) (SubscriptionInfo, error) {
	raw, err := c.QueryRender(ctx, "json/sub/"+strconv.FormatUint(subID, 10))
	if err != nil {
		return SubscriptionInfo{}, err
	}
	if strings.Contains(raw, `"error":"not_found"`) {
		return SubscriptionInfo{}, fmt.Errorf("subscription %d not found", subID)
	}
	var out SubscriptionInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return SubscriptionInfo{}, err
	}
	return out, nil
}

func (c *RPCClient) Receipt(ctx context.Context, idOrHash string) (ReceiptInfo, error) {
	raw, err := c.QueryRender(ctx, "json/receipt/"+idOrHash)
	if err != nil {
		return ReceiptInfo{}, err
	}
	if strings.Contains(raw, `"error":"not_found"`) {
		return ReceiptInfo{}, fmt.Errorf("receipt %q not found", idOrHash)
	}
	var out ReceiptInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ReceiptInfo{}, err
	}
	return out, nil
}

func (c *RPCClient) CheckPassQuota(ctx context.Context, passID uint64, units int64) (bool, error) {
	res, err := c.QueryEval(ctx, fmt.Sprintf("CheckPassQuota(%d, %d)", passID, units))
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(res), "(true bool)"), nil
}

func (c *RPCClient) CheckSubscriptionActive(ctx context.Context, subID uint64, units int64) (bool, error) {
	res, err := c.QueryEval(ctx, fmt.Sprintf("CheckSubscriptionActive(%d, %d)", subID, units))
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(res), "(true bool)"), nil
}
