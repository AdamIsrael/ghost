package store

import (
	"database/sql"
	"errors"
	"strings"
)

// LabelState is what Ghost knows about a review label on a PR.
type LabelState int

const (
	LabelNever    LabelState = iota // never seen on the PR or added by Ghost
	LabelAdded                      // was on the PR (added by Ghost or seen there)
	LabelOptedOut                   // removed after that: don't review
)

// LabelState reports whether label was ever on the PR, and whether it was
// removed since. Labels compare case-insensitively.
func (s *Store) LabelState(prID int64, label string) (LabelState, error) {
	var optedOut sql.NullString
	err := s.db.QueryRow(`SELECT opted_out_at FROM pr_labels WHERE pr_id = ? AND label = ?`,
		prID, strings.ToLower(label)).Scan(&optedOut)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return LabelNever, nil
	case err != nil:
		return LabelNever, err
	case optedOut.Valid:
		return LabelOptedOut, nil
	}
	return LabelAdded, nil
}

// MarkLabeled records that label is (or was) on the PR. It never clears an
// opt-out; use ClearOptOut for that.
func (s *Store) MarkLabeled(prID int64, label string) error {
	_, err := s.db.Exec(`INSERT INTO pr_labels (pr_id, label, added_at) VALUES (?, ?, ?)
		ON CONFLICT (pr_id, label) DO NOTHING`, prID, strings.ToLower(label), ts(s.now()))
	return err
}

// MarkOptedOut records that a recorded label was removed. It reports whether
// anything changed, so the opt-out is audited once.
func (s *Store) MarkOptedOut(prID int64, label string) (bool, error) {
	res, err := s.db.Exec(`UPDATE pr_labels SET opted_out_at = ?
		WHERE pr_id = ? AND label = ? AND opted_out_at IS NULL`, ts(s.now()), prID, strings.ToLower(label))
	return changed(res, err)
}

// ClearOptOut records that the label was added back. It reports whether
// anything changed.
func (s *Store) ClearOptOut(prID int64, label string) (bool, error) {
	res, err := s.db.Exec(`UPDATE pr_labels SET opted_out_at = NULL
		WHERE pr_id = ? AND label = ? AND opted_out_at IS NOT NULL`, prID, strings.ToLower(label))
	return changed(res, err)
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
