package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BackupDatabase creates a backup copy of the SQLite database file.
func (d *Database) BackupDatabase(backupDir string) (string, error) {
	// Use SQLite's VACUUM INTO for a consistent backup
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	backupName := fmt.Sprintf("feedbacks_%s.db", time.Now().Format("20060102_150405"))
	backupPath := filepath.Join(backupDir, backupName)

	d.mu.Lock()
	defer d.mu.Unlock()

	// VACUUM INTO requires a string literal, not a bound parameter
	safePath := strings.ReplaceAll(backupPath, "'", "''")
	_, err := d.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", safePath))
	if err != nil {
		return "", fmt.Errorf("vacuum into backup: %w", err)
	}

	return backupPath, nil
}

// ArchiveOldFeedbacks closes old pending feedbacks. If projectID is non-empty, only
// feedbacks belonging to that project are affected.
func (d *Database) ArchiveOldFeedbacks(daysOld int, projectID string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -daysOld).Unix()
	var res sql.Result
	var err error
	if projectID != "" {
		res, err = d.db.Exec(`UPDATE feedbacks SET status = 'closed', updated_at = strftime('%s', 'now') WHERE status IN ('pending', 'processing') AND created_at < ? AND project_id = ?`, cutoff, projectID)
	} else {
		res, err = d.db.Exec(`UPDATE feedbacks SET status = 'closed', updated_at = strftime('%s', 'now') WHERE status IN ('pending', 'processing') AND created_at < ?`, cutoff)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneOldBackups removes backup files older than the specified number of days.
func (d *Database) PruneOldBackups(backupDir string, daysOld int) (int, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -daysOld)
	pruned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Prefer the timestamp embedded in the filename
		// (feedbacks_YYYYMMDD_HHMMSS.db or .db.enc) over ModTime, which can be
		// altered by rsync, manual touch, or filesystem restores.
		if t, ok := parseBackupTime(name); ok {
			if t.Before(cutoff) {
				os.Remove(filepath.Join(backupDir, name))
				pruned++
			}
			continue
		}
		// Fallback to ModTime if the name doesn't match the expected pattern.
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(backupDir, name))
			pruned++
		}
	}
	return pruned, nil
}

// parseBackupTime extracts the backup timestamp embedded in a backup filename of
// the form feedbacks_YYYYMMDD_HHMMSS.db (optionally .enc).
func parseBackupTime(name string) (time.Time, bool) {
	const prefix = "feedbacks_"
	if !strings.HasPrefix(name, prefix) {
		return time.Time{}, false
	}
	rest := name[len(prefix):]
	rest = strings.TrimSuffix(rest, ".enc")
	rest = strings.TrimSuffix(rest, ".db")
	ts, err := time.Parse("20060102_150405", rest)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
