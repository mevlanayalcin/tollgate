package tollgate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var reTxHash = regexp.MustCompile(`TX HASH:\s*(\S+)`)

// GnokeySubmitter settles verified gateway receipts directly on-chain via gnokey.
type GnokeySubmitter struct {
	GnokeyBin string
	KeyName   string
	KeyHome   string
	Password  string
	RPCURL    string
	ChainID   string
	RealmPath string
	GasFee    string
	GasWanted int64
}

func (g *GnokeySubmitter) SubmitReceipt(ctx context.Context, r PendingReceipt) (string, error) {
	gasFee := g.GasFee
	if gasFee == "" {
		gasFee = "1000000ugnot"
	}
	gasWanted := g.GasWanted
	if gasWanted == 0 {
		gasWanted = 20000000
	}
	args := []string{
		"maketx", "call", g.KeyName,
		"-home", g.KeyHome,
		"-insecure-password-stdin",
		"-remote", g.RPCURL,
		"-chainid", g.ChainID,
		"-broadcast",
		"-gas-fee", gasFee,
		"-gas-wanted", strconv.FormatInt(gasWanted, 10),
		"-pkgpath", g.RealmPath,
	}

	switch r.Rail {
	case "metered-pass":
		args = append(args,
			"-func", "ClaimSettlement",
			"-args", strconv.FormatUint(r.PassID, 10),
			"-args", strconv.FormatInt(r.Units, 10),
			"-args", r.Nonce,
			"-args", r.ResourceHash,
			"-args", r.BuyerSigHex,
		)
	case "subscription":
		args = append(args,
			"-func", "ConsumeSubscription",
			"-args", strconv.FormatUint(r.SubID, 10),
			"-args", strconv.FormatInt(r.Units, 10),
			"-args", r.Nonce,
			"-args", r.ResourceHash,
			"-args", r.BuyerSigHex,
		)
	case "x402-direct":
		txh := r.TxHash
		if len(txh) < 8 {
			txh = "x402-" + r.Nonce
		}
		args = append(args,
			"-func", "RecordX402Receipt",
			"-args", strconv.FormatUint(r.ServiceID, 10),
			"-args", r.Buyer,
			"-args", strconv.FormatInt(r.Units, 10),
			"-args", strconv.FormatInt(r.AmountUgnot, 10),
			"-args", r.Nonce,
			"-args", r.ResourceHash,
			"-args", txh,
		)
	default:
		return "", fmt.Errorf("unsupported rail %q", r.Rail)
	}

	cmd := exec.CommandContext(ctx, g.GnokeyBin, args...)
	cmd.Stdin = strings.NewReader(g.Password + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gnokey %s failed: %w: %s", r.Rail, err, out.String())
	}
	if m := reTxHash.FindStringSubmatch(out.String()); len(m) == 2 {
		return m[1], nil
	}
	return "", nil
}
