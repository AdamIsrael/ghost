package github

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// defaultRetryDelays are the waits before the second and third attempt of a
// request that failed transiently: brief blips (a 502 from GitHub, a dropped
// connection) usually clear within seconds.
var defaultRetryDelays = []time.Duration{time.Second, 2 * time.Second}

// retry runs do, retrying transient failures after each of the client's
// retry delays. It stops early when ctx is done.
func (c *Client) retry(ctx context.Context, do func() error) error {
	delays := c.retryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	err := do()
	for _, d := range delays {
		if err == nil || !transient(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
		err = do()
	}
	return err
}

// transient reports failures worth retrying: GitHub server errors (5xx) and
// network errors. 4xx responses (bad query, no access, rate limits) and
// GraphQL errors won't change on a retry.
func transient(err error) bool {
	var h *api.HTTPError
	if errors.As(err, &h) {
		return h.StatusCode >= 500
	}
	var g *api.GraphQLError
	if errors.As(err, &g) {
		return false
	}
	var n net.Error
	return errors.As(err, &n)
}
