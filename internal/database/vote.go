package database

import (
	"strings"
)

// defaultVoteTarget is the target_type for legacy feedback votes. FAQ votes use
// "faq". Keeping both in one table means the same numeric id can be voted on as
// a feedback and as a FAQ independently — the PRIMARY KEY (feedback_id,
// voter_key, vote_type, target_type) guarantees they never collide.
const defaultVoteTarget = "feedback"

// InsertVote records a vote, deduplicated by
// (feedback_id, voter_key, vote_type, target_type). targetType defaults to
// "feedback" for backward compatibility. Returns (alreadyVoted bool, err).
func (d *Database) InsertVote(feedbackID int64, voterKey, voteType, targetType string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if voteType == "" {
		voteType = "useful"
	}
	if targetType == "" {
		targetType = defaultVoteTarget
	}
	res, err := d.db.Exec(`INSERT OR IGNORE INTO feedback_votes (feedback_id, voter_key, vote_type, target_type) VALUES (?, ?, ?, ?)`, feedbackID, voterKey, voteType, targetType)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected == 0, nil
}

// CountVotes returns the total number of feedback votes (target_type='feedback').
func (d *Database) CountVotes(feedbackID int64) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedback_votes WHERE feedback_id = ? AND target_type = ?`, feedbackID, defaultVoteTarget).Scan(&n)
	return n, err
}

// CountVotesByType returns the number of votes of a specific type for a target.
// targetType defaults to "feedback" when empty, so existing callers keep working.
func (d *Database) CountVotesByType(feedbackID int64, voteType, targetType string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if voteType == "" {
		voteType = "useful"
	}
	if targetType == "" {
		targetType = defaultVoteTarget
	}
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedback_votes WHERE feedback_id = ? AND target_type = ? AND vote_type = ?`, feedbackID, targetType, voteType).Scan(&n)
	return n, err
}

// VoteCountMap returns a map of feedback_id -> vote count (target_type='feedback')
// for the given ids, so feedback listings never pick up FAQ votes.
func (d *Database) VoteCountMap(ids []int64) (map[int64]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[int64]int, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE target_type = ? AND feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, append([]interface{}{defaultVoteTarget}, args...)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			continue
		}
		out[id] = n
	}
	return out, nil
}
