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

func TestCrossLanguagePayWordGoldenVector(t *testing.T) {
	got := PayWordStep("onyx-1", "0000000000000000000000000000000000000000000000000000000000000000")
	want := "33166b8a509fc02566aa139ef29415c7458dc917ddd3a559f42649d59faf2d05"
	if got != want {
		t.Fatalf("PayWordStep golden mismatch: got %s want %s", got, want)
	}
}
