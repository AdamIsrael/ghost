package config

// DefaultMaxPRs is how many PRs a view shows unless max_prs_per_view or the
// view's max_prs says otherwise. MaxPRsLimit is the most GitHub returns in
// one search request.
const (
	DefaultMaxPRs = 50
	MaxPRsLimit   = 100
)

// PRLimitFor returns how many PRs view v fetches: its own max_prs if set,
// otherwise max_prs_per_view (or the default when that is unset).
func (c *Config) PRLimitFor(v View) int {
	if v.MaxPRs > 0 {
		return v.MaxPRs
	}
	if c.MaxPRsPerView > 0 {
		return c.MaxPRsPerView
	}
	return DefaultMaxPRs
}
