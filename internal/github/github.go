// Package github queries GitHub through the user's gh authentication.
package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/status"
)

// Search result limits: GitHub returns at most 100 results per request.
const (
	defaultSearchLimit = 50
	maxSearchLimit     = 100
)

type PR struct {
	Repo           string
	Number         int
	State          string // OPEN, CLOSED or MERGED
	Title          string
	URL            string
	Author         string
	Body           string
	BaseRef        string
	HeadRef        string
	HeadSHA        string
	IsDraft        bool
	ReviewDecision string
	CIState        string
	Additions      int
	Deletions      int
	ChangedFiles   int
	UpdatedAt      time.Time
	Labels         []string
}

func (p PR) Status() status.Status { return status.Derive(p.IsDraft, p.ReviewDecision, p.CIState) }

// HasLabel reports whether the PR has the named label, ignoring case as
// GitHub does.
func (p PR) HasLabel(name string) bool {
	for _, l := range p.Labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// GraphQL is the subset of go-gh's GraphQLClient that Client uses.
type GraphQL interface {
	DoWithContext(ctx context.Context, query string, variables map[string]interface{}, response interface{}) error
}

type Client struct {
	gql  GraphQL
	rest REST

	now     func() time.Time // for the probe cache; nil = time.Now
	probeMu sync.Mutex
	probed  map[string]probeResult // query → why it matched nothing
}

func New(gql GraphQL) *Client { return &Client{gql: gql} }

// NewDefault builds a Client from the user's gh auth configuration.
func NewDefault() (*Client, error) {
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, err
	}
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	return New(gql).WithREST(rest), nil
}

const searchQuery = `query($q: String!, $first: Int!) {
  search(query: $q, type: ISSUE, first: $first) {
    issueCount
    nodes {
      ... on PullRequest {
        number state title url body isDraft updatedAt additions deletions changedFiles
        baseRefName headRefName headRefOid reviewDecision
        author { login }
        repository { nameWithOwner }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
        labels(first: 50) { nodes { name } }
      }
    }
  }
}`

type searchResponse struct {
	Search struct {
		IssueCount int      `json:"issueCount"`
		Nodes      []prNode `json:"nodes"`
	} `json:"search"`
}

type prNode struct {
	Number         int       `json:"number"`
	State          string    `json:"state"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	Body           string    `json:"body"`
	IsDraft        bool      `json:"isDraft"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changedFiles"`
	BaseRefName    string    `json:"baseRefName"`
	HeadRefName    string    `json:"headRefName"`
	HeadRefOid     string    `json:"headRefOid"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// PartialError reports PRs a search matched but GitHub would not return, for
// example PRs in organizations that enforce SAML for the user's token.
type PartialError struct {
	Hidden int
	Reason string
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%d PRs hidden: %s", e.Hidden, e.Reason)
}

// partial converts a GraphQL error whose items all point at individual search
// results into a PartialError. Any other error is not partial.
func partial(err error) *PartialError {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return nil
	}
	for _, item := range gqlErr.Errors {
		if len(item.Path) < 3 || item.Path[0] != "search" || item.Path[1] != "nodes" {
			return nil
		}
	}
	return &PartialError{Hidden: len(gqlErr.Errors), Reason: gqlErr.Errors[0].Message}
}

// SearchPRs runs a GitHub search query and returns up to limit matching pull
// requests (1–100; out-of-range values are clamped, 0 means 50) and the total
// number of matches. Non-PR results (issues) are dropped. If GitHub withholds
// some results, the visible PRs are returned along with a *PartialError; if a
// search matches nothing because of access (e.g. SAML SSO), an empty list is
// returned with a *SearchBlockedError.
func (c *Client) SearchPRs(ctx context.Context, query string, limit int) ([]PR, int, error) {
	switch {
	case limit < 1:
		limit = defaultSearchLimit
	case limit > maxSearchLimit:
		limit = maxSearchLimit
	}
	var resp searchResponse
	vars := map[string]interface{}{"q": query, "first": limit}
	err := c.gql.DoWithContext(ctx, searchQuery, vars, &resp)
	hidden := partial(err)
	if err != nil && hidden == nil {
		return nil, 0, fmt.Errorf("search: %w", err)
	}
	prs := make([]PR, 0, len(resp.Search.Nodes))
	for _, n := range resp.Search.Nodes {
		if n.Number == 0 {
			continue
		}
		prs = append(prs, toPR(n))
	}
	if hidden != nil {
		return prs, resp.Search.IssueCount, hidden
	}
	if resp.Search.IssueCount == 0 && len(prs) == 0 {
		// GraphQL search drops results the token can't see without saying
		// so; REST search names the problem. prs stays non-nil, so this is
		// a warning rather than a failed poll.
		if why := c.explainEmpty(ctx, query); why != nil {
			return prs, 0, why
		}
	}
	return prs, resp.Search.IssueCount, nil
}

func toPR(n prNode) PR {
	pr := PR{
		Repo: n.Repository.NameWithOwner, Number: n.Number, State: n.State, Title: n.Title, URL: n.URL,
		Author: "ghost", Body: n.Body, BaseRef: n.BaseRefName, HeadRef: n.HeadRefName,
		HeadSHA: n.HeadRefOid, IsDraft: n.IsDraft, ReviewDecision: n.ReviewDecision,
		Additions: n.Additions, Deletions: n.Deletions, ChangedFiles: n.ChangedFiles,
		UpdatedAt: n.UpdatedAt,
	}
	if n.Author != nil {
		pr.Author = n.Author.Login
	}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		pr.CIState = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	for _, l := range n.Labels.Nodes {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

// PRDetail is a PR plus the paths it changes (first 100).
type PRDetail struct {
	PR
	Files []string
}

const prQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number state title url body isDraft updatedAt additions deletions changedFiles
      baseRefName headRefName headRefOid reviewDecision
      author { login }
      repository { nameWithOwner }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      labels(first: 50) { nodes { name } }
      files(first: 100) { nodes { path } }
    }
  }
}`

type prResponse struct {
	Repository *struct {
		PullRequest *struct {
			prNode
			Files struct {
				Nodes []struct {
					Path string `json:"path"`
				} `json:"nodes"`
			} `json:"files"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// GetPR fetches one pull request and its changed file paths.
func (c *Client) GetPR(ctx context.Context, repo string, number int) (PRDetail, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return PRDetail{}, fmt.Errorf("repo %q is not owner/name", repo)
	}
	var resp prResponse
	vars := map[string]interface{}{"owner": owner, "name": name, "number": number}
	if err := c.gql.DoWithContext(ctx, prQuery, vars, &resp); err != nil {
		return PRDetail{}, fmt.Errorf("get %s#%d: %w", repo, number, err)
	}
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return PRDetail{}, fmt.Errorf("%s#%d not found", repo, number)
	}
	n := resp.Repository.PullRequest
	d := PRDetail{PR: toPR(n.prNode)}
	for _, f := range n.Files.Nodes {
		d.Files = append(d.Files, f.Path)
	}
	return d, nil
}

var (
	prURL   = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)(?:[/?#].*)?$`)
	prShort = regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`)
)

// ParsePRRef accepts a GitHub PR URL or owner/name#number.
func ParsePRRef(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	m := prURL.FindStringSubmatch(s)
	if m == nil {
		m = prShort.FindStringSubmatch(s)
	}
	if m == nil {
		return "", 0, fmt.Errorf("%q is not a PR URL or owner/repo#number", s)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("%q has an invalid PR number", s)
	}
	return m[1], n, nil
}
