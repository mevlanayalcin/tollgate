// Command tollgate-gateway runs a live dual-rail HTTP 402 (gnolang/x402) +
// Tollgate on-chain pass/subscription API gateway and web playground server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tollgate "github.com/mevlanayalcin/tollgate/sdk/go"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:8402", "HTTP listen address")
	rpcURL := flag.String("rpc", "https://rpc.onyx.testnets.gno.land:443", "Gno RPC URL")
	realmPath := flag.String("realm", "", "Tollgate realm path (e.g. gno.land/r/<addr>/tollgate)")
	chainID := flag.String("chain-id", "onyx-1", "Gno chain ID")
	facilitatorURL := flag.String("facilitator", "https://facilitator.onyx.testnets.gno.land", "gnolang/x402 facilitator URL")
	payTo := flag.String("pay-to", "", "Provider payTo address")
	serviceID := flag.Uint64("service-id", 1, "On-chain Tollgate service ID")
	unitPrice := flag.Int64("unit-price", 1000, "Unit price in ugnot")
	webDir := flag.String("web", "web", "Static web playground directory")
	stateFile := flag.String("state-file", "", "Optional JSON file to persist used nonces and unsettled receipts")
	requireAuth := flag.Bool("require-auth", true, "Require Ed25519 buyer voucher signatures on pass and subscription rails")
	autoSettle := flag.Duration("auto-settle", 0, "Optional background on-chain settlement flush interval (e.g. 15s; 0 disables timer)")
	gnokeyBin := flag.String("gnokey-bin", "", "Path to gnokey binary to enable on-chain settlement submission")
	keyName := flag.String("key-name", "deployer", "gnokey key name for provider settlement txs")
	keyHome := flag.String("key-home", "", "gnokey home directory")
	keyPassFile := flag.String("key-pass-file", "", "File containing gnokey key password")
	flag.Parse()

	if *realmPath == "" {
		log.Fatal("--realm is required")
	}
	if *payTo == "" {
		log.Fatal("--pay-to is required")
	}

	chainClient := tollgate.NewRPCClient(*rpcURL, *realmPath)
	fac := tollgate.NewHTTPFacilitator(*facilitatorURL)

	var submitter tollgate.SettlementSubmitter
	if *gnokeyBin != "" && *keyHome != "" && *keyPassFile != "" {
		pwBytes, err := os.ReadFile(*keyPassFile)
		if err != nil {
			log.Fatalf("read --key-pass-file: %v", err)
		}
		submitter = &tollgate.GnokeySubmitter{
			GnokeyBin: *gnokeyBin,
			KeyName:   *keyName,
			KeyHome:   *keyHome,
			Password:  strings.TrimRight(string(pwBytes), "\r\n"),
			RPCURL:    *rpcURL,
			ChainID:   *chainID,
			RealmPath: *realmPath,
		}
	}

	mw := tollgate.NewMiddleware(tollgate.MiddlewareConfig{
		ChainID:     *chainID,
		RealmPath:   *realmPath,
		Chain:       chainClient,
		Facilitator: fac,
		Submitter:   submitter,
		StateFile:   *stateFile,
		RequireAuth: *requireAuth,
		Routes: map[string]tollgate.RouteSpec{
			"GET /api/v1/oracle": {
				ServiceID:      *serviceID,
				Slug:           "alpha-oracle",
				Description:    "Verified live market oracle quote metered via Tollgate + x402",
				PayTo:          *payTo,
				UnitPriceUgnot: *unitPrice,
				MimeType:       "application/json",
			},
			"GET /api/v1/weather": {
				ServiceID:      *serviceID,
				Slug:           "alpha-oracle",
				Description:    "Deterministic meteorological snapshot metered via Tollgate + x402",
				PayTo:          *payTo,
				UnitPriceUgnot: *unitPrice,
				MimeType:       "application/json",
			},
			"POST /api/v1/inference": {
				ServiceID:      *serviceID,
				Slug:           "alpha-oracle",
				Description:    "AI agent structured extraction endpoint metered via Tollgate + x402",
				PayTo:          *payTo,
				UnitPriceUgnot: *unitPrice,
				MimeType:       "application/json",
			},
		},
	})

	apiMux := http.NewServeMux()

	// Metered endpoints (wrapped by dual-rail middleware)
	apiMux.HandleFunc("GET /api/v1/oracle", func(w http.ResponseWriter, r *http.Request) {
		pair := r.URL.Query().Get("pair")
		if pair == "" {
			pair = "GNOT-USD"
		}
		now := time.Now().UTC()
		price := 1.40 + 0.05*math.Sin(float64(now.Unix()/60))
		writeJSON(w, http.StatusOK, map[string]any{
			"pair":      pair,
			"price":     math.Round(price*10000) / 10000,
			"source":    "tollgate-verified-oracle",
			"timestamp": now.Format(time.RFC3339),
			"chainId":   *chainID,
			"realm":     *realmPath,
		})
	})

	apiMux.HandleFunc("GET /api/v1/weather", func(w http.ResponseWriter, r *http.Request) {
		city := r.URL.Query().Get("city")
		if city == "" {
			city = "zurich"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"city":        city,
			"tempC":       12.4,
			"condition":   "clear",
			"windKph":     9.2,
			"observedAt":  time.Now().UTC().Format(time.RFC3339),
			"meteredRail": "tollgate-x402",
		})
	})

	apiMux.HandleFunc("POST /api/v1/inference", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		words := strings.Fields(body.Prompt)
		writeJSON(w, http.StatusOK, map[string]any{
			"model":       "tollgate-agent-router-v1",
			"tokenCount":  len(words),
			"promptHash":  tollgate.HashResource(body.Prompt),
			"completedAt": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Free health, explorer proxy, and settlement control endpoints
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/", mw.Wrap(apiMux))

	rootMux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                    true,
			"chainId":               *chainID,
			"realm":                 *realmPath,
			"rpc":                   *rpcURL,
			"facilitator":           *facilitatorURL,
			"serviceId":             *serviceID,
			"requireAuth":           *requireAuth,
			"autoSettlerConfigured": submitter != nil,
		})
	})

	rootMux.HandleFunc("GET /api/explorer/summary", func(w http.ResponseWriter, r *http.Request) {
		sum, err := chainClient.Summary(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, sum)
	})

	rootMux.HandleFunc("GET /api/explorer/services", func(w http.ResponseWriter, r *http.Request) {
		svcs, err := chainClient.Services(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, svcs)
	})

	rootMux.HandleFunc("GET /api/explorer/pass/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid pass id"})
			return
		}
		p, err := chainClient.Pass(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	rootMux.HandleFunc("GET /api/explorer/receipt/{id}", func(w http.ResponseWriter, r *http.Request) {
		rec, err := chainClient.Receipt(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	rootMux.HandleFunc("GET /api/explorer/pending-receipts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, mw.Receipts())
	})

	rootMux.HandleFunc("POST /api/settle-now", func(w http.ResponseWriter, r *http.Request) {
		if submitter == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "on-chain settler not configured (pass --gnokey-bin, --key-home, --key-pass-file)",
			})
			return
		}
		n, err := mw.FlushSettlements(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"settled":  n,
				"error":    err.Error(),
				"receipts": mw.Receipts(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"settled":  n,
			"receipts": mw.Receipts(),
		})
	})

	rootMux.HandleFunc("POST /api/verify-receipt", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedHex  string `json:"expectedHex"`
			ChainID      string `json:"chainId"`
			ServiceID    uint64 `json:"serviceId"`
			PassID       uint64 `json:"passId"`
			Buyer        string `json:"buyer"`
			Provider     string `json:"provider"`
			Units        int64  `json:"units"`
			AmountUgnot  int64  `json:"amountUgnot"`
			Nonce        string `json:"nonce"`
			ResourceHash string `json:"resourceHash"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		recomputed := tollgate.ReceiptCommitment(
			req.ChainID, req.ServiceID, req.PassID, req.Buyer, req.Provider,
			req.Units, req.AmountUgnot, req.Nonce, req.ResourceHash,
		)
		valid := tollgate.VerifyReceiptCommitment(
			req.ExpectedHex, req.ChainID, req.ServiceID, req.PassID, req.Buyer, req.Provider,
			req.Units, req.AmountUgnot, req.Nonce, req.ResourceHash,
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":      valid,
			"recomputed": recomputed,
		})
	})

	if _, err := os.Stat(*webDir); err == nil {
		rootMux.Handle("/", http.FileServer(http.Dir(*webDir)))
	}

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           withCORS(rootMux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *autoSettle > 0 && submitter != nil {
		go func() {
			ticker := time.NewTicker(*autoSettle)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					flushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					if n, err := mw.FlushSettlements(flushCtx); err != nil {
						log.Printf("auto-settle error (settled=%d): %v", n, err)
					} else if n > 0 {
						log.Printf("auto-settle flushed %d receipt(s) on-chain", n)
					}
					cancel()
				}
			}
		}()
	}

	go func() {
		log.Printf("tollgate-gateway listening on http://%s (chain=%s realm=%s auth=%v settler=%v)",
			*listenAddr, *chainID, *realmPath, *requireAuth, submitter != nil)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println("tollgate-gateway stopped")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, PAYMENT-SIGNATURE, X-Tollgate-Pass, X-Tollgate-Sub, X-Tollgate-Units, X-Tollgate-Nonce, X-Tollgate-Voucher-Sig")
		w.Header().Set("Access-Control-Expose-Headers", "PAYMENT-REQUIRED, PAYMENT-RESPONSE, X-Tollgate-Rail, X-Tollgate-Receipt-Commitment, X-Tollgate-Nonce, X-Tollgate-Resource-Hash, X-Tollgate-Service-Id, X-Tollgate-Realm, X-Tollgate-Net-Ugnot")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
