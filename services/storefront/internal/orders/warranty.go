package orders

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CurrentWarranty reads the product setting for every order, including old orders.
// Lock the plan while creating a refund so a concurrent settings change is serialized.
func CurrentWarranty(ctx context.Context, q Query, plan string, lock bool) (bool, error) {
	sql := `SELECT supports_warranty FROM catalog_plans WHERE id=$1`
	if lock {
		sql += ` FOR SHARE`
	}
	var supported bool
	err := q.QueryRow(ctx, sql, plan).Scan(&supported)
	if errors.Is(err, pgx.ErrNoRows) {
		// Retired products without a catalog entry have no configured exclusion.
		return true, nil
	}
	return supported, err
}
