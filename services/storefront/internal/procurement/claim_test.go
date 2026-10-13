package procurement

import (
	"gptibo/storefront/internal/delivery"
	"testing"
)

func TestGeminiKnownClaimHostUsesHTTPS(t *testing.T) {
	const service = "DEMO_PRODUCT_A"
	input := "http://claim.example.invalid/claim/a%2Fb?token=x%2By&next=%2F#claim"
	want := "https://claim.example.invalid/claim/a%2Fb?token=x%2By&next=%2F#claim"
	got := delivery_supplierDeliveryValue(service, input)
	if got != want || !delivery.ValidValue("link", got) {
		t.Fatal("claim bytes changed", got)
	}
	for _, value := range []string{
		"http://unverified.example/claim",
		"http://claim.example.invalid.evil.test/claim",
		"http://claim.example.invalid:8080/claim",
		"http://user@claim.example.invalid/claim",
		"http://claim.example.invalid/claim\nother",
		"http://claim.example.invalid/claim\"onclick=evil",
	} {
		if delivery.ValidValue("link", delivery_supplierDeliveryValue(service, value)) {
			t.Fatal("accepted unsafe claim", value)
		}
	}
	if delivery_supplierDeliveryValue("another-service", input) != input {
		t.Fatal("changed another product")
	}
	if delivery_supplierDeliveryValue(service, want) != want {
		t.Fatal("changed HTTPS claim")
	}
}
