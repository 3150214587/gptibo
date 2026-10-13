package management

import (
	"context"
	"gptibo/storefront/internal/orders"
	"net/url"
	"strings"
)

// Store links share the same currently routable origin as the account links.
func (s Service) referralOrigin(ctx context.Context, q orders.Query, id string) (string, error) {
	link, _, err := s.partnerLinks(ctx, q, id, s.Origin)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", err
	}
	origin := u.Scheme + "://" + u.Host
	if u.Host == "" || origin == strings.TrimRight(s.Origin, "/") {
		return "", nil
	}
	return origin, nil
}
