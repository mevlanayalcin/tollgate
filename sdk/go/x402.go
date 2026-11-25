package tollgate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	HeaderPaymentRequired  = "PAYMENT-REQUIRED"
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"
	HeaderPaymentResponse  = "PAYMENT-RESPONSE"

	HeaderTollgatePass       = "X-Tollgate-Pass"
	HeaderTollgateSub        = "X-Tollgate-Sub"
	HeaderTollgateUnits      = "X-Tollgate-Units"
	HeaderTollgateNonce      = "X-Tollgate-Nonce"
	HeaderTollgateRail       = "X-Tollgate-Rail"
	HeaderTollgateCommitment = "X-Tollgate-Receipt-Commitment"
	HeaderTollgateResHash    = "X-Tollgate-Resource-Hash"
	HeaderTollgateServiceID  = "X-Tollgate-Service-Id"
	HeaderTollgateRealm      = "X-Tollgate-Realm"
	HeaderTollgateNetUgnot   = "X-Tollgate-Net-Ugnot"
)

// PaymentRequired mirrors the x402 v2 PAYMENT-REQUIRED JSON schema used by gnolang/x402.
type PaymentRequired struct {
	X402Version int                   `json:"x402Version"`
	Error       string                `json:"error"`
	Resource    X402Resource          `json:"resource"`
	Accepts     []PaymentRequirements `json:"accepts"`
}

type X402Resource struct {
	URL         string `json:"url"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

type PaymentRequirements struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`
	Asset             string         `json:"asset"`
	Amount            string         `json:"amount"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
	Extra             map[string]any `json:"extra,omitempty"`
}

type VerifyResponse struct {
	IsValid       bool   `json:"isValid"`
	InvalidReason string `json:"invalidReason,omitempty"`
	Payer         string `json:"payer,omitempty"`
}

type SettleResponse struct {
	Success       bool   `json:"success"`
	ErrorReason   string `json:"errorReason,omitempty"`
	Transaction   string `json:"transaction,omitempty"`
	Network       string `json:"network,omitempty"`
	Payer         string `json:"payer,omitempty"`
}

func EncodePaymentRequired(pr PaymentRequired) (string, error) {
	raw, err := json.Marshal(pr)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func DecodePaymentRequired(headerVal string) (PaymentRequired, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(headerVal))
	if err != nil {
		return PaymentRequired{}, err
	}
	var pr PaymentRequired
	if err := json.Unmarshal(raw, &pr); err != nil {
		return PaymentRequired{}, err
	}
	return pr, nil
}

// Facilitator abstracts gnolang/x402's HTTP facilitator (/verify and /settle).
type Facilitator interface {
	Verify(ctx context.Context, paymentHeader string, req PaymentRequirements) (VerifyResponse, error)
	Settle(ctx context.Context, paymentHeader string, req PaymentRequirements) (SettleResponse, error)
}

type HTTPFacilitator struct {
	BaseURL string
	HTTP    *http.Client
}

func NewHTTPFacilitator(baseURL string) *HTTPFacilitator {
	return &HTTPFacilitator{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (f *HTTPFacilitator) call(ctx context.Context, endpoint, paymentHeader string, req PaymentRequirements, out any) error {
	rawPayload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(paymentHeader))
	if err != nil {
		return fmt.Errorf("invalid base64 PAYMENT-SIGNATURE: %w", err)
	}
	var paymentPayload any
	if err := json.Unmarshal(rawPayload, &paymentPayload); err != nil {
		return fmt.Errorf("invalid JSON PAYMENT-SIGNATURE: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"x402Version":         2,
		"paymentPayload":      paymentPayload,
		"paymentRequirements": req,
	})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.BaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := f.HTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("facilitator %s status %d: %s", endpoint, resp.StatusCode, string(respBody))
	}
	return json.Unmarshal(respBody, out)
}

func (f *HTTPFacilitator) Verify(ctx context.Context, paymentHeader string, req PaymentRequirements) (VerifyResponse, error) {
	var out VerifyResponse
	err := f.call(ctx, "/verify", paymentHeader, req, &out)
	return out, err
}

func (f *HTTPFacilitator) Settle(ctx context.Context, paymentHeader string, req PaymentRequirements) (SettleResponse, error) {
	var out SettleResponse
	err := f.call(ctx, "/settle", paymentHeader, req, &out)
	return out, err
}
