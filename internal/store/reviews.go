package store

import (
	"database/sql"
	"errors"
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
)

// tsFormat is fixed-width UTC so timestamps sort as strings.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

type Review struct {
	ID         int64
	PRID       int64
	Repo       string
	Number     int
	HeadSHA    string
	Agent      string
	State      string
	Prompt     string
	Verdict    string
	Summary    string
	Comments   string // JSON array of agent.InlineComment
	Error      string
	CostUSD    float64
	CreatedAt  time.Time
	FinishedAt time.Time // set once the review stops running
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
	r.summary, r.comments, r.error, r.cost_usd, r.created_at, COALESCE(r.finished_at, '')`

func scanReview(row interface{ Scan(...any) error }) (*Review, error) {
	var r Review
	var created, finished string
	err := row.Scan(&r.ID, &r.PRID, &r.Repo, &r.Number, &r.HeadSHA, &r.Agent, &r.State, &r.Prompt, &r.Verdict,
		&r.Summary, &r.Comments, &r.Error, &r.CostUSD, &created, &finished)
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
func (s *Store) ClaimNext() (*Review, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := scanReview(tx.QueryRow(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id
		WHERE r.state = ? ORDER BY r.created_at, r.id LIMIT 1`, Queued))
	if err != nil || r == nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE reviews SET state = ? WHERE id = ?`, Running, r.ID); err != nil {
		return nil, err
	}
	r.State = Running
	return r, tx.Commit()
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
		error = ?, cost_usd = ?, finished_at = ? WHERE id = ?`,
		r.State, r.Prompt, r.Verdict, r.Summary, r.Comments, r.Error, r.CostUSD, finished, r.ID)
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

// ResetRunning requeues reviews left running by a previous process.
func (s *Store) ResetRunning() (int64, error) {
	res, err := s.db.Exec(`UPDATE reviews SET state = ? WHERE state = ?`, Queued, Running)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
