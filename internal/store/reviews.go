package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/AdamIsrael/ghost/internal/github"
)

// Review states.
const (
	Queued  = "queued"
	Running = "running"
	Ready   = "ready" // finished; waiting to be posted
	Posted  = "posted"
	Blocked = "blocked" // never posted: possible secret in output
	Failed  = "failed"
	// Posting is held while a post is in flight, so only one caller posts.
	Posting = "posting"
	// PostUnknown: the post may or may not have reached GitHub (timeout,
	// 5xx, or exit mid-post). Posting again first checks GitHub.
	PostUnknown = "post-unknown"
)

// Approval states (Review.Approval). Empty means approval wasn't considered.
const (
	ApprovalWaiting   = "waiting"   // not yet: CI, draft or a human's requested changes
	ApprovalPending   = "pending"   // qualifies; waiting for the operator (key a)
	ApprovalApproving = "approving" // approval in flight
	ApprovalApproved  = "approved"
	ApprovalNo        = "not eligible" // final for this commit
	ApprovalFailed    = "failed"       // GitHub refused (4xx), e.g. own PR
	ApprovalUnknown   = "unknown"      // may have reached GitHub; check before retrying
)

// tsFormat is fixed-width UTC so timestamps sort as strings.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

type Review struct {
	ID           int64
	PRID         int64
	Repo         string
	Number       int
	HeadSHA      string
	Agent        string
	State        string
	Prompt       string
	Verdict      string
	Summary      string
	Comments     string // JSON array of agent.InlineComment
	Error        string
	CostUSD      float64
	Approval     string
	ApprovalNote string
	CreatedAt    time.Time
	FinishedAt   time.Time // set once the review stops running
}

type AuditEvent struct {
	ID       int64
	ReviewID int64 // 0 when not tied to a review
	At       time.Time
	Kind     string
	Message  string
}

func (s *Store) UpsertPR(pr github.PR) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO prs (repo, number, title, author, url, head_sha, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo, number) DO UPDATE SET title = excluded.title, author = excluded.author,
			url = excluded.url, head_sha = excluded.head_sha, status = excluded.status, updated_at = excluded.updated_at
		RETURNING id`,
		pr.Repo, pr.Number, pr.Title, pr.Author, pr.URL, pr.HeadSHA, pr.Status().String(), ts(s.now())).Scan(&id)
	return id, err
}

// EnqueueReview queues a review of headSHA. Unless force is set, it does
// nothing if any review of that head exists, whatever its state — a failed
// review may have cost money, so it is not retried automatically.
func (s *Store) EnqueueReview(prID int64, headSHA, agentName string, force bool) (int64, bool, error) {
	if !force {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE pr_id = ? AND head_sha = ?`, prID, headSHA).Scan(&n); err != nil {
			return 0, false, err
		}
		if n > 0 {
			return 0, false, nil
		}
	}
	res, err := s.db.Exec(`INSERT INTO reviews (pr_id, head_sha, agent, state, created_at) VALUES (?, ?, ?, ?, ?)`,
		prID, headSHA, agentName, Queued, ts(s.now()))
	if err != nil {
		return 0, false, err
	}
	id, err := res.LastInsertId()
	return id, err == nil, err
}

const reviewCols = `r.id, r.pr_id, p.repo, p.number, r.head_sha, r.agent, r.state, r.prompt, r.verdict,
	r.summary, r.comments, r.error, r.cost_usd, r.created_at, COALESCE(r.finished_at, ''), r.approval, r.approval_note`

func scanReview(row interface{ Scan(...any) error }) (*Review, error) {
	var r Review
	var created, finished string
	err := row.Scan(&r.ID, &r.PRID, &r.Repo, &r.Number, &r.HeadSHA, &r.Agent, &r.State, &r.Prompt, &r.Verdict,
		&r.Summary, &r.Comments, &r.Error, &r.CostUSD, &created, &finished, &r.Approval, &r.ApprovalNote)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(tsFormat, created)
	if finished != "" {
		r.FinishedAt, _ = time.Parse(tsFormat, finished)
	}
	return &r, nil
}

// ClaimNext marks the oldest queued review running and returns it, or nil.
// The claim is one UPDATE so it takes SQLite's write lock up front: a
// read-then-write transaction would fail with SQLITE_BUSY_SNAPSHOT under
// concurrent writers instead of waiting on busy_timeout.
func (s *Store) ClaimNext() (*Review, error) {
	var id int64
	err := s.db.QueryRow(`UPDATE reviews SET state = ?
		WHERE id = (SELECT id FROM reviews WHERE state = ? ORDER BY created_at, id LIMIT 1)
		RETURNING id`, Running, Queued).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return scanReview(s.db.QueryRow(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id WHERE r.id = ?`, id))
}

// SaveReview writes a review's mutable fields. Leaving the running state
// stamps finished_at once, which is when its cost counts toward the day.
func (s *Store) SaveReview(r *Review) error {
	var finished sql.NullString
	if r.State != Queued && r.State != Running {
		if r.FinishedAt.IsZero() {
			r.FinishedAt = s.now()
		}
		finished = sql.NullString{String: ts(r.FinishedAt), Valid: true}
	}
	if r.Comments == "" {
		r.Comments = "[]"
	}
	_, err := s.db.Exec(`UPDATE reviews SET state = ?, prompt = ?, verdict = ?, summary = ?, comments = ?,
		error = ?, cost_usd = ?, finished_at = ?, approval = ?, approval_note = ? WHERE id = ?`,
		r.State, r.Prompt, r.Verdict, r.Summary, r.Comments, r.Error, r.CostUSD, finished, r.Approval, r.ApprovalNote, r.ID)
	return err
}

// LatestReview returns the most recent review of a PR, or nil.
func (s *Store) LatestReview(repo string, number int) (*Review, error) {
	return scanReview(s.db.QueryRow(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE p.repo = ? AND p.number = ? ORDER BY r.id DESC LIMIT 1`, repo, number))
}

func (s *Store) CountState(state string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE state = ?`, state).Scan(&n)
	return n, err
}

// SpendSince sums the cost of reviews that finished at or after t.
func (s *Store) SpendSince(t time.Time) (float64, error) {
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM reviews WHERE finished_at >= ?`, ts(t)).Scan(&sum)
	return sum, err
}

// FailInterrupted settles reviews a previous process left in flight. A
// running review may have spent money with no cost report, so it fails and is
// charged cap (it is not retried automatically); a posting one becomes
// PostUnknown. It returns the affected reviews.
func (s *Store) FailInterrupted(cap float64) ([]Review, error) {
	rows, err := s.db.Query(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id WHERE r.state IN (?, ?)`, Running, Posting)
	if err != nil {
		return nil, err
	}
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *r)
	}
	rows.Close()
	for i := range out {
		r := &out[i]
		if r.State == Running {
			r.State, r.Error = Failed, "interrupted: ghost exited while it was running"
			r.CostUSD = max(r.CostUSD, cap)
		} else {
			r.State = PostUnknown
		}
		if err := s.SaveReview(r); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.Exec(`UPDATE reviews SET approval = ? WHERE approval = ?`, ApprovalUnknown, ApprovalApproving); err != nil {
		return nil, err
	}
	return out, nil
}

// TransitionApproval moves a review's approval state only if it is still
// from; it reports whether it did.
func (s *Store) TransitionApproval(id int64, from, to string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reviews SET approval = ? WHERE id = ? AND approval = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ApprovalsFor returns the reviews of the given PRs whose approval is in one
// of states.
func (s *Store) ApprovalsFor(prs []github.PR, states ...string) ([]Review, error) {
	if len(prs) == 0 || len(states) == 0 {
		return nil, nil
	}
	var where []string
	var args []any
	for _, pr := range prs {
		where = append(where, "(p.repo = ? AND p.number = ?)")
		args = append(args, pr.Repo, pr.Number)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
	for _, st := range states {
		args = append(args, st)
	}
	rows, err := s.db.Query(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE (`+strings.Join(where, " OR ")+`) AND r.approval IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// TransitionState moves a review from one state to another only if it is
// still in from; it reports whether it did.
func (s *Store) TransitionState(id int64, from, to string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reviews SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) AddAudit(prID, reviewID int64, kind, msg string) error {
	var rid sql.NullInt64
	if reviewID != 0 {
		rid = sql.NullInt64{Int64: reviewID, Valid: true}
	}
	_, err := s.db.Exec(`INSERT INTO audit_events (pr_id, review_id, kind, message, created_at) VALUES (?, ?, ?, ?, ?)`,
		prID, rid, kind, msg, ts(s.now()))
	return err
}

// Audit returns a PR's last limit events, oldest first.
func (s *Store) Audit(repo string, number, limit int) ([]AuditEvent, error) {
	rows, err := s.db.Query(`SELECT id, review_id, created_at, kind, message FROM (
		SELECT a.id, COALESCE(a.review_id, 0) AS review_id, a.created_at, a.kind, a.message
		FROM audit_events a JOIN prs p ON p.id = a.pr_id
		WHERE p.repo = ? AND p.number = ? ORDER BY a.id DESC LIMIT ?) ORDER BY id`, repo, number, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at string
		if err := rows.Scan(&e.ID, &e.ReviewID, &at, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(tsFormat, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SupersedeApprovals marks the waiting or pending approvals of a PR's other
// reviews not eligible (a newer review replaces them) and returns them.
func (s *Store) SupersedeApprovals(prID, keepID int64, note string) ([]Review, error) {
	rows, err := s.db.Query(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE r.pr_id = ? AND r.id != ? AND r.approval IN (?, ?)`, prID, keepID, ApprovalWaiting, ApprovalPending)
	if err != nil {
		return nil, err
	}
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *r)
	}
	rows.Close()
	for i := range out {
		if _, err := s.TransitionApproval(out[i].ID, out[i].Approval, ApprovalNo); err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`UPDATE reviews SET approval_note = ? WHERE id = ?`, note, out[i].ID); err != nil {
			return nil, err
		}
		out[i].Approval, out[i].ApprovalNote = ApprovalNo, note
	}
	return out, nil
}

// HeadApproved reports whether another review of the same PR commit is
// approved by Ghost, or may be (in flight or unknown).
func (s *Store) HeadApproved(prID int64, headSHA string, exceptID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE pr_id = ? AND head_sha = ? AND id != ? AND approval IN (?, ?, ?)`,
		prID, headSHA, exceptID, ApprovalApproved, ApprovalApproving, ApprovalUnknown).Scan(&n)
	return n > 0, err
}

// UpdateApproval sets a review's approval state and note only if its
// approval is still from; it reports whether it did.
func (s *Store) UpdateApproval(id int64, from, to, note string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reviews SET approval = ?, approval_note = ? WHERE id = ? AND approval = ?`, to, note, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
