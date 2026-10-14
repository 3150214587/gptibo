package xgift

import "testing"

func TestSupplyLimitAcceptsConfiguredQuotesAndRejectsUnboundedCost(t *testing.T) {
	s := &Service{SupplyLimits: map[string]int64{"x-premium-3m": 237}}
	for _, p := range []int64{1, 137, 237} {
		if !s.supplyAllowed("x-premium-3m", "platform", p) {
			t.Fatal("valid configured quote rejected", p)
		}
	}
	for _, p := range []int64{-1, 0, 238, 1700} {
		if s.supplyAllowed("x-premium-3m", "platform", p) {
			t.Fatal("out-of-cap quote accepted", p)
		}
	}
	if s.supplyAllowed("x-premium-6m", "platform", 1) || (&Service{}).supplyAllowed("x-premium-3m", "platform", 1) {
		t.Fatal("unconfigured limit accepted")
	}
}
