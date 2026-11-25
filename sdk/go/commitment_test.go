package tollgate

import "testing"

func TestCrossLanguageReceiptCommitmentVector(t *testing.T) {
	resHash := HashResource("GET /v1/weather?city=zurich")
	got := ReceiptCommitment(
		"onyx-1",
		1,
		7,
		"g1buyer000000000000000000000000000000000",
		"g1prov0000000000000000000000000000000000",
		10,
		9500,
		"inv-0001",
		resHash,
	)
	if !IsValidHexDigest(got) {
		t.Fatalf("invalid digest: %q", got)
	}
	if !VerifyReceiptCommitment(
		got, "onyx-1", 1, 7,
		"g1buyer000000000000000000000000000000000",
		"g1prov0000000000000000000000000000000000",
		10, 9500, "inv-0001", resHash,
	) {
		t.Fatal("VerifyReceiptCommitment returned false")
	}
	t.Logf("CANONICAL_VECTOR_RES=%s", resHash)
	t.Logf("CANONICAL_VECTOR_COMMITMENT=%s", got)
}

func TestChargeAndMemo(t *testing.T) {
	c, err := ComputeCharge(1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if c.NetUgnot != 9500 || c.DiscountBps != 500 {
		t.Fatalf("unexpected charge: %+v", c)
	}
	memo, err := FormatInvoiceMemo(3, "req_01")
	if err != nil {
		t.Fatal(err)
	}
	id, nonce, ok := ParseInvoiceMemo(memo)
	if !ok || id != 3 || nonce != "req_01" {
		t.Fatalf("memo parse mismatch: %d %q %v", id, nonce, ok)
	}
}
