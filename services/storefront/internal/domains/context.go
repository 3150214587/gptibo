package domains

import "context"

type Scope struct{ Host, PartnerID, Slug, OwnerID string }

func (s Scope) Owner() string {
	if s.OwnerID != "" {
		return s.OwnerID
	}
	return s.PartnerID
}

// Preserve existing root-shop sessions, but separate child storefront buyers.
func (s Scope) AuthScope(role string) string {
	if role != "partner" && s.Host != "" && s.PartnerID != s.Owner() {
		return s.Host + "/" + s.PartnerID
	}
	return s.Host
}

type scopeKey struct{}

func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func Current(ctx context.Context) Scope { s, _ := ctx.Value(scopeKey{}).(Scope); return s }
