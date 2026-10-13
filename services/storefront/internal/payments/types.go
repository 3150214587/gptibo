package payments

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type Order struct {
	ID, Subject     string
	TradeNo         string
	Amount, Expires int64
}
type Result struct {
	OrderID, Status, TradeNo string
	Amount                   int64
}
type Refund struct {
	ID     string
	Amount int64
}
type Refunded struct {
	Confirmed bool
	TradeNo   string
}
type Provider interface {
	ID() string
	Account() string
	Create(context.Context, Order, string) (string, error)
	Query(context.Context, Order) (Result, error)
	Close(context.Context, Order) (bool, error)
	Refund(context.Context, Order, Refund) (Refunded, error)
	QueryRefund(context.Context, Order, Refund) (Refunded, error)
	Notification(*http.Request) (Result, error)
}

var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,7})(\.[0-9]{1,2})?$`)

func AmountCents(raw string) (int64, error) {
	if !amountPattern.MatchString(raw) {
		return 0, fmt.Errorf("invalid payment amount")
	}
	parts := strings.Split(raw, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	var fraction int64
	if len(parts) == 2 {
		v := parts[1]
		if len(v) == 1 {
			v += "0"
		}
		fraction, _ = strconv.ParseInt(v, 10, 64)
	}
	n := whole*100 + fraction
	if n < 1 || n > 10000000 {
		return 0, fmt.Errorf("invalid payment amount")
	}
	return n, nil
}
func AmountText(n int64) (string, error) {
	if n < 1 || n > 10000000 {
		return "", fmt.Errorf("invalid payment amount")
	}
	return fmt.Sprintf("%d.%02d", n/100, n%100), nil
}
