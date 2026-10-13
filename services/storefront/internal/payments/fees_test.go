package payments

import "testing"

func TestFeeRoundingAndRefundConservation(t *testing.T) {
	for _, c := range []struct{ base, bps, want int64 }{{10000, 140, 140}, {9900, 140, 139}, {1, 140, 0}, {50, 100, 1}, {10000, 0, 0}} {
		fee, e := FeeCents(c.base, c.bps)
		if e != nil || fee != c.want {
			t.Fatal(c, fee, e)
		}
		var sum int64
		for refunded := int64(0); refunded < c.base; refunded++ {
			f, e := RefundFee(c.base, fee, refunded, 1)
			if e != nil {
				t.Fatal(e)
			}
			sum += f
		}
		if sum != fee {
			t.Fatal("split refunds lost fee cents", sum, fee)
		}
	}
	for _, c := range [][2]int64{{-1, 140}, {10000, -1}, {10000, 10001}, {10000000, 140}, {10000001, 0}} {
		if _, e := FeeCents(c[0], c[1]); e == nil {
			t.Fatal("invalid fee accepted", c)
		}
	}
	for _, c := range [][4]int64{{10000, 140, 9900, 101}, {10000, 140, 0, 0}, {10000, 140, -1, 100}} {
		if _, e := RefundFee(c[0], c[1], c[2], c[3]); e == nil {
			t.Fatal("invalid refund accepted")
		}
	}
}
