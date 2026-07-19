package database

import (
	"strings"
)

// InsertVote records a vote, deduplicated by (feedback_id, voter_key).
// Returns (alreadyVoted bool, err). If already voted, no new row is inserted.
func (d *Database) InsertVote(feedbackID int64, voterKey string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`INSERT OR IGNORE INTO feedback_votes (feedback_id, voter_key) VALUES (?, ?)`, feedbackID, voterKey)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected == 0, nil
}

// CountVotes returns the number of votes for a feedback.
func (d *Database) CountVotes(feedbackID int64) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedback_votes WHERE feedback_id = ?`, feedbackID).Scan(&n)
	return n, err
}

// VoteCountMap returns a map of feedback_id -> vote count for the given ids.
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
	rows, err := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
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
