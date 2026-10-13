package procurement

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDeliverySupplierExactAmountsAndCeilings(t *testing.T) {
	for raw, want := range map[string]int64{"0": 0, "0.445": 445000, "0.38": 380000, "0.000001": 1, "20.123456": 20123456} {
		v := json.Number(raw)
		got, e := usdMicros(&v)
		if e != nil || got != want {
			t.Fatal(raw, got, e)
		}
	}
	for _, raw := range []string{"-1", "NaN", "Infinity", "0.4450001", "100000001", "1/2", "1e999"} {
		v := json.Number(raw)
		if _, e := usdMicros(&v); e == nil {
			t.Fatal("invalid amount accepted", raw)
		}
	}
	if _, e := usdMicros(nil); e == nil {
		t.Fatal("missing amount accepted")
	}
	for raw, valid := range map[string]bool{"0.445": true, "0.6": true, "0.600001": false, "0": false} {
		v := json.Number(raw)
		e := validateNY([]NYProduct{{ID: "service", Name: "Gemini", Price: &v, Stock: 1}}, "service", 60)
		if (e == nil) != valid {
			t.Fatal("ceiling rounded", raw, e)
		}
	}
}

func TestDeliverySupplierStockFailureIsDistinctFromInvalidQuote(t *testing.T) {
	price := json.Number("0.41")
	for _, tc := range []struct {
		name, availability string
		stock              int64
		price              *json.Number
		outOfStock, valid  bool
	}{
		{"zero stock", "available", 0, &price, true, false},
		{"sold out", "sold_out", 10, &price, true, false},
		{"available", "available", 1, &price, false, true},
		{"legacy available", "", 1, &price, false, true},
		{"invalid stock", "available", -1, &price, false, false},
		{"invalid price", "available", 1, nil, false, false},
		{"disabled", "disabled", 10, &price, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNY([]NYProduct{{ID: "service", Name: "Gemini", Price: tc.price, Stock: tc.stock, Availability: tc.availability}}, "service", 60)
			if errors.Is(err, ErrOutOfStock) != tc.outOfStock || (err == nil) != tc.valid {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
	if err := validateNY([]NYProduct{{ID: "another", Stock: 0}}, "service", 60); err == nil || errors.Is(err, ErrOutOfStock) {
		t.Fatal("missing product incorrectly classified as out of stock", err)
	}
}
