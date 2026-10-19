package github

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// SearchBlockedError explains why a search returned nothing: GitHub's GraphQL
// search silently drops what the token can't see (an organization enforcing
// SAML single sign-on, a repository that doesn't exist or isn't visible),
// while its REST search reports it.
type SearchBlockedError struct {
	Reason string
	SAML   bool // the token isn't authorized for an organization's SSO
}

func (e *SearchBlockedError) Error() string {
	if e.SAML {
		return e.Reason + " Run `gh auth refresh -h github.com` and authorize the organization for SSO."
	}
	return e.Reason
}

// explainEmpty asks GitHub's REST search why query matched nothing. It returns
// nil when there's nothing to explain or the probe itself fails.
func (c *Client) explainEmpty(ctx context.Context, query string) error {
	if c.rest == nil {
		return nil
	}
	path := "search/issues?" + url.Values{"q": {query}, "per_page": {"1"}}.Encode()
	var discard struct{}
	err := c.rest.DoWithContext(ctx, "GET", path, nil, &discard)
	var h *api.HTTPError
	if !errors.As(err, &h) || h.StatusCode != 422 {
		return nil
	}
	var reasons []string
	for _, item := range h.Errors {
		if m := strings.TrimSpace(item.Message); m != "" {
			reasons = append(reasons, m)
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	reason := strings.Join(reasons, " ")
	return &SearchBlockedError{Reason: reason, SAML: strings.Contains(reason, "SAML")}
}
