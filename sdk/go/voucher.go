package tollgate

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

const (
	PayWordDomain   = "tollgate-payword-v1"
	MaxPayWordSteps = 1024
)

// PayWordStep computes one forward step of a Rivest-Shamir PayWord SHA-256 hash chain.
// Matches gno.land/r/.../tollgate/v2.PayWordStep byte-for-byte.
func PayWordStep(chainID, prevHex string) string {
	sum := sha256.Sum256([]byte(PayWordDomain + "|" + chainID + "|" + prevHex))
	return hex.EncodeToString(sum[:])
}

// PayWordChain holds a precomputed SHA-256 hash chain w_0..w_depth where
// w_i = PayWordStep(chainID, w_{i-1}) and AnchorHex() == w_depth.
type PayWordChain struct {
	ChainID string
	SeedHex string
	Depth   int
	Hashes  []string
}

// NewPayWordSeed generates a cryptographically random 32-byte seed hex.
func NewPayWordSeed() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// BuildPayWordChain derives w_0..w_depth from seedHex for chainID.
func BuildPayWordChain(chainID, seedHex string, depth int) (*PayWordChain, error) {
	if !IsValidHexDigest(seedHex) {
		return nil, fmt.Errorf("tollgate: invalid 64-char hex seed")
	}
	if depth <= 0 || depth > 100000 {
		return nil, fmt.Errorf("tollgate: payword depth out of range (1..100000)")
	}
	hashes := make([]string, depth+1)
	hashes[0] = seedHex
	for i := 1; i <= depth; i++ {
		hashes[i] = PayWordStep(chainID, hashes[i-1])
	}
	return &PayWordChain{
		ChainID: chainID,
		SeedHex: seedHex,
		Depth:   depth,
		Hashes:  hashes,
	}, nil
}

// AnchorHex returns the initial on-chain root w_depth to anchor in OpenPass / Subscribe.
func (c *PayWordChain) AnchorHex() string {
	return c.Hashes[c.Depth]
}

// ProofForAnchor locates currentAnchorHex inside the chain and returns the preimage
// authorizing exactly `units` steps forward (w_{pos - units}).
func (c *PayWordChain) ProofForAnchor(currentAnchorHex string, units int64) (string, int, error) {
	if units <= 0 || units > MaxPayWordSteps {
		return "", -1, fmt.Errorf("tollgate: units out of PayWord bounds (1..%d)", MaxPayWordSteps)
	}
	for pos := c.Depth; pos >= 0; pos-- {
		if c.Hashes[pos] == currentAnchorHex {
			target := pos - int(units)
			if target < 0 {
				return "", pos, fmt.Errorf("tollgate: PayWord chain exhausted (remaining=%d < units=%d)", pos, units)
			}
			return c.Hashes[target], target, nil
		}
	}
	return "", -1, fmt.Errorf("tollgate: current anchor %q not found in PayWord chain (depth=%d)", currentAnchorHex, c.Depth)
}

// VerifyPayWord checks that hashing preimageHex `steps` times reproduces anchorHex.
// Matches gno.land/r/.../tollgate/v2.verifyPayWord byte-for-byte.
func VerifyPayWord(chainID, anchorHex, preimageHex string, steps int64) bool {
	if steps <= 0 || steps > MaxPayWordSteps {
		return false
	}
	if !IsValidHexDigest(anchorHex) || !IsValidHexDigest(preimageHex) {
		return false
	}
	cur := preimageHex
	for i := int64(0); i < steps; i++ {
		cur = PayWordStep(chainID, cur)
	}
	return subtle.ConstantTimeCompare([]byte(cur), []byte(anchorHex)) == 1
}
