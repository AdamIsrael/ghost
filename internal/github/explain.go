package github

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

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
		return e.Reason + " Authorize your gh token for this organization's SSO: run `gh auth refresh` and grant access, or use GitHub Settings → Applications → Authorized OAuth Apps → GitHub CLI."
	}
	return e.Reason
}

// probeTTL is how long an explanation (or the lack of one) is reused, so
// views that stay empty don't spend REST search's 30-requests-a-minute limit
// on every poll.
const probeTTL = 5 * time.Minute

type probeResult struct {
	err error
	at  time.Time
}

// scopedQuery matches searches that name an owner or repository: only those
// can come back empty because of access.
var scopedQuery = regexp.MustCompile(`(?i)(^|\s)-?(org|repo|user|owner):`)

// explainEmpty asks GitHub's REST search why query matched nothing. It returns
// nil when there's nothing to explain or the probe itself fails. Results are
// cached per query for probeTTL.
func (c *Client) explainEmpty(ctx context.Context, query string) error {
	if c.rest == nil || ctx.Err() != nil || !scopedQuery.MatchString(query) {
		return nil
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.probeMu.Lock()
	if r, ok := c.probed[query]; ok && now().Sub(r.at) < probeTTL {
		c.probeMu.Unlock()
		return r.err
	}
	c.probeMu.Unlock()

	why := c.probe(ctx, query)
	if ctx.Err() != nil {
		return nil // cancelled mid-probe: don't cache
	}
	c.probeMu.Lock()
	if c.probed == nil {
		c.probed = map[string]probeResult{}
	}
	c.probed[query] = probeResult{err: why, at: now()}
	c.probeMu.Unlock()
	return why
}

func (c *Client) probe(ctx context.Context, query string) error {
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
