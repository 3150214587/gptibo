package recharge

import (
	"context"
	"gptibo/storefront/internal/catalog"
)

// Browsing does not contact suppliers. Snapshot checks live supply and price bounds.
func (s *Service) Availability(_ context.Context, plans []catalog.Plan) []catalog.Plan {
	for i := range plans {
		if !s.Checkout || s.Client == nil || s.Products[plans[i].ID] == "" {
			plans[i].Enabled = 0
		}
	}
	return plans
}
