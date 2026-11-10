package store

import (
	"database/sql"
	"errors"
)

// Manual approvals are the ones the operator asks for with no review behind
// them (key a on any PR). One row per PR commit, using the Approval* states.

// ManualApproval returns the state of the manual approval of a PR commit, or
// "" if there is none.
func (s *Store) ManualApproval(prID int64, headSHA string) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM manual_approvals WHERE pr_id = ? AND head_sha = ?`, prID, headSHA).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return state, err
}

// ClaimManualApproval marks a PR commit as being approved by hand, if it has
// no manual approval yet or its last one is in state from (e.g. failed). It
// reports whether it did, so two presses can't both approve.
func (s *Store) ClaimManualApproval(prID int64, headSHA, from string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO manual_approvals (pr_id, head_sha, state, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (pr_id, head_sha) DO UPDATE SET state = excluded.state, note = '', updated_at = excluded.updated_at
		WHERE manual_approvals.state = ?`, prID, headSHA, ApprovalApproving, ts(s.now()), from)
	return changed(res, err)
}

// SetManualApproval records the outcome of a manual approval.
func (s *Store) SetManualApproval(prID int64, headSHA, state, note string) error {
	_, err := s.db.Exec(`UPDATE manual_approvals SET state = ?, note = ?, updated_at = ? WHERE pr_id = ? AND head_sha = ?`,
		state, note, ts(s.now()), prID, headSHA)
	return err
}
