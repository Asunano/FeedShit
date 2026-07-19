package database

import (
	"database/sql"
	"strings"
	"time"
)

// buildAccessPlanWhere constructs a SQL WHERE clause from a ProjectAccess slice.
// Projects with nil AllowedCategories (wildcard '*') use simple project_id filter.
// Projects with specific categories use (project_id = ? AND category IN (...)).
// All conditions are OR'd together.
func buildAccessPlanWhere(plan []ProjectAccess) (string, []interface{}) {
	if len(plan) == 0 {
		return " WHERE 1=0", nil // no access
	}

	var orConditions []string
	args := []interface{}{}
	var unrestrictedSlugs []string

	for _, pa := range plan {
		if pa.AllowedCategories == nil {
			unrestrictedSlugs = append(unrestrictedSlugs, pa.Slug)
		} else if len(pa.AllowedCategories) > 0 {
			placeholders := make([]string, len(pa.AllowedCategories))
			for i, cat := range pa.AllowedCategories {
				placeholders[i] = "?"
				args = append(args, cat)
			}
			orConditions = append(orConditions, "(category IN ("+strings.Join(placeholders, ",")+") AND project_id = ?)")
			args = append(args, pa.Slug)
		}
	}

	// Build unrestricted slugs condition
	if len(unrestrictedSlugs) == 1 {
		orConditions = append([]string{"project_id = ?"}, orConditions...)
		args = append([]interface{}{unrestrictedSlugs[0]}, args...)
	} else if len(unrestrictedSlugs) > 1 {
		placeholders := make([]string, len(unrestrictedSlugs))
		slugArgs := make([]interface{}, len(unrestrictedSlugs))
		for i, s := range unrestrictedSlugs {
			placeholders[i] = "?"
			slugArgs[i] = s
		}
		orConditions = append([]string{"project_id IN (" + strings.Join(placeholders, ",") + ")"}, orConditions...)
		args = append(slugArgs, args...)
	}

	if len(orConditions) == 0 {
		return " WHERE 1=0", nil
	}
	if len(orConditions) == 1 {
		return " WHERE " + orConditions[0], args
	}
	return " WHERE (" + strings.Join(orConditions, " OR ") + ")", args
}

// InsertFeedback inserts a new feedback record and returns its ID.
func (d *Database) InsertFeedback(f *Feedback) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	status := f.Status
	if status == "" {
		status = "pending"
	}
	res, err := d.db.Exec(
		`INSERT INTO feedbacks (project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, category, content_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))`,
		f.ProjectID, f.Title, f.Description, f.CustomData, f.FilePaths, f.ClientIP, status, f.Tags, f.Assignee, f.ContactName, f.ContactEmail, f.TrackingToken, f.Priority, f.Category, ComputeContentHash(f.Title, f.Description),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	f.ID = id
	f.CreatedAt = time.Now()
	return id, nil
}

// ListFeedbacks returns feedbacks filtered by project_id (empty = all), paginated.
// limit is automatically clamped to [1, 500] to prevent uncontrolled queries.
func (d *Database) ListFeedbacks(projectIDs []string, accessPlan []ProjectAccess, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Safety clamp: limit must be in [1, 500]
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	var rows *sql.Rows
	var err error

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash`

	where := ""
	args := []interface{}{}

	if accessPlan != nil {
		where, args = buildAccessPlanWhere(accessPlan)
	} else if len(projectIDs) == 1 {
		where = ` WHERE project_id = ?`
		args = append(args, projectIDs[0])
	} else if len(projectIDs) > 1 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where = ` WHERE project_id IN (` + strings.Join(placeholders, ",") + `)`
	}

	err = d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks`+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, limit, offset)
	rows, err = d.db.Query(
		`SELECT `+cols+`, public_on_roadmap, roadmap_status FROM feedbacks`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		var isPublic int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &isPublic, &f.RoadmapStatus); err != nil {
			return nil, 0, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.PublicOnRoadmap = isPublic == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	if len(list) > 0 {
		ids := make([]int64, len(list))
		ph := make([]string, len(list))
		args := make([]interface{}, len(list))
		for i, f := range list {
			ids[i] = f.ID
			ph[i] = "?"
			args[i] = f.ID
		}
		vrows, verr := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
		if verr == nil {
			defer vrows.Close()
			vmap := make(map[int64]int, len(list))
			for vrows.Next() {
				var vid int64
				var n int
				if vrows.Scan(&vid, &n) == nil {
					vmap[vid] = n
				}
			}
			for i := range list {
				list[i].Votes = vmap[list[i].ID]
			}
		}
	}
	return list, total, nil
}

// SearchFeedbacks supports keyword search across multiple fields, status/priority/assignee filters, and project filter.
// limit is automatically clamped to [1, 500] to prevent uncontrolled queries.
func (d *Database) SearchFeedbacks(projectIDs []string, accessPlan []ProjectAccess, keyword, status, priority, assignee, category, trackingToken string, limit, offset int) ([]Feedback, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Safety clamp: limit must be in [1, 500]
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	where := "WHERE 1=1"
	args := []interface{}{}

	if accessPlan != nil {
		planWhere, planArgs := buildAccessPlanWhere(accessPlan)
		// Replace "WHERE 1=1" with the access plan WHERE clause
		if planWhere != "" {
			// planWhere is " WHERE (...)" — append as AND condition
			where += " AND" + strings.TrimPrefix(planWhere, " WHERE")
			args = append(args, planArgs...)
		}
	} else if len(projectIDs) == 1 {
		where += " AND project_id = ?"
		args = append(args, projectIDs[0])
	} else if len(projectIDs) > 1 {
		placeholders := make([]string, len(projectIDs))
		for i, pid := range projectIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where += " AND project_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	if priority != "" {
		where += " AND priority = ?"
		args = append(args, priority)
	}
	if assignee != "" {
		where += " AND assignee = ?"
		args = append(args, assignee)
	}
	if category != "" {
		where += " AND category = ?"
		args = append(args, category)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		where += ` AND (title LIKE ? OR description LIKE ? OR tags LIKE ? OR contact_name LIKE ? OR contact_email LIKE ? OR id IN (SELECT feedback_id FROM feedback_notes WHERE content LIKE ?))`
		args = append(args, like, like, like, like, like, like)
	}
	if trackingToken != "" {
		where += " AND tracking_token = ?"
		args = append(args, trackingToken)
	}

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash`

	var total int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM feedbacks `+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, limit, offset)
	rows, err := d.db.Query(
		`SELECT `+cols+`, public_on_roadmap, roadmap_status FROM feedbacks `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		var isPublic int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &isPublic, &f.RoadmapStatus); err != nil {
			return nil, 0, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.PublicOnRoadmap = isPublic == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	if len(list) > 0 {
		ids := make([]int64, len(list))
		ph := make([]string, len(list))
		args := make([]interface{}, len(list))
		for i, f := range list {
			ids[i] = f.ID
			ph[i] = "?"
			args[i] = f.ID
		}
		vrows, verr := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
		if verr == nil {
			defer vrows.Close()
			vmap := make(map[int64]int, len(list))
			for vrows.Next() {
				var vid int64
				var n int
				if vrows.Scan(&vid, &n) == nil {
					vmap[vid] = n
				}
			}
			for i := range list {
				list[i].Votes = vmap[list[i].ID]
			}
		}
	}
	return list, total, nil
}

// GetFeedback returns a single feedback by ID.
func (d *Database) GetFeedback(id int64) (*Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var f Feedback
	var createdAt int64
	var isDuplicate int
	var isPublic int
	err := d.db.QueryRow(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash, public_on_roadmap, roadmap_status
		 FROM feedbacks WHERE id = ?`, id,
	).Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &isPublic, &f.RoadmapStatus)
	if err != nil {
		return nil, err
	}
	f.IsDuplicate = isDuplicate == 1
	f.PublicOnRoadmap = isPublic == 1
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

// GetTags returns distinct tag values matching the given prefix, limited to 20.
// Used for tag autocomplete in the admin UI.
func (d *Database) GetTags(prefix string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT DISTINCT tags FROM feedbacks WHERE tags != '' AND tags LIKE ? ORDER BY tags LIMIT 20`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []string
	seen := map[string]bool{}
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			continue
		}
		// tags is comma-separated; split and deduplicate
		for _, t := range strings.Split(val, ",") {
			t = strings.TrimSpace(t)
			if t != "" && strings.HasPrefix(strings.ToLower(t), strings.ToLower(prefix)) && !seen[t] {
				seen[t] = true
				list = append(list, t)
			}
		}
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}

// MergeFeedback moves notes and votes from sourceID to targetID when
// source is marked as a duplicate of target. This consolidated the data
// under the target feedback so merging feels seamless.
func (d *Database) MergeFeedback(sourceID, targetID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Move notes from source to target
	if _, err := d.db.Exec(`UPDATE feedback_notes SET feedback_id = ? WHERE feedback_id = ?`, targetID, sourceID); err != nil {
		return err
	}
	// Remove source votes (they would conflict with the same voting key on target)
	if _, err := d.db.Exec(`DELETE FROM feedback_votes WHERE feedback_id = ?`, sourceID); err != nil {
		return err
	}
	return nil
}

// GetFeedbacksByIDs returns all feedbacks matching the given IDs (batch query).
// Used for bulk RBAC checks to avoid N+1 queries.
func (d *Database) GetFeedbacksByIDs(ids []int64) ([]Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	rows, err := d.db.Query(
		`SELECT id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash, public_on_roadmap, roadmap_status
		 FROM feedbacks WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		var isPublic int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &isPublic, &f.RoadmapStatus); err != nil {
			return nil, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.PublicOnRoadmap = isPublic == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}
	return list, nil
}

// UpdateFeedbackStatus updates the status and/or tags of a feedback.
func (d *Database) UpdateFeedbackStatus(id int64, status, tags string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET status = ?, tags = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, status, tags, id)
	return err
}

// DeleteFeedback removes a feedback record by ID.
func (d *Database) DeleteFeedback(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cascade delete notes first
	d.db.Exec(`DELETE FROM feedback_notes WHERE feedback_id = ?`, id)
	_, err := d.db.Exec(`DELETE FROM feedbacks WHERE id = ?`, id)
	return err
}

// GetAssignees returns distinct non-empty assignee values.
func (d *Database) GetAssignees() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT DISTINCT assignee FROM feedbacks WHERE assignee != '' ORDER BY assignee`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, nil
}

// BackfillContentHashes 存量回填：仅对 content_hash 为空的行按 title+description 计算写入。
// 在 migrate() 末尾调用一次；幂等——只处理空值行，重复运行为空操作。持 Database.mu 锁。
func (d *Database) BackfillContentHashes() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.db.Query(`SELECT id, title, description FROM feedbacks WHERE content_hash = '' OR content_hash IS NULL`)
	if err != nil {
		return err
	}
	type row struct {
		id    int64
		title string
		desc  string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title, &r.desc); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	rows.Close()

	for _, r := range pending {
		hash := ComputeContentHash(r.title, r.desc)
		if _, err := d.db.Exec(`UPDATE feedbacks SET content_hash = ? WHERE id = ?`, hash, r.id); err != nil {
			return err
		}
	}
	return nil
}

// FindExactDuplicates 精确指纹匹配：同 project、开放态、未合并、排除自身，按时间倒序，LIMIT。
// 仅比对 pending/processing 且与目标不同的开放反馈；project_id 为硬约束。
func (d *Database) FindExactDuplicates(projectID, hash string, excludeID int64, limit int) ([]Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT id, project_id, title, description, status, tracking_token, content_hash, is_duplicate, duplicate_of
		FROM feedbacks
		WHERE project_id = ?
		  AND content_hash = ?
		  AND id != ?
		  AND is_duplicate = 0
		  AND status IN ('pending', 'processing')
		ORDER BY created_at DESC
		LIMIT ?`, projectID, hash, excludeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var isDuplicate int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.Status, &f.TrackingToken, &f.ContentHash, &isDuplicate, &f.DuplicateOf); err != nil {
			return nil, err
		}
		f.IsDuplicate = isDuplicate == 1
		list = append(list, f)
	}
	return list, nil
}

// ExportFeedbacks returns all feedbacks for a project (or all if projectID is empty) for CSV export.
func (d *Database) ExportFeedbacks(projectID string) ([]Feedback, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	const cols = `id, project_id, title, description, custom_data, file_paths, client_ip, status, tags, assignee, contact_name, contact_email, tracking_token, priority, is_duplicate, duplicate_of, category, created_at, updated_at, content_hash, public_on_roadmap, roadmap_status`

	var rows *sql.Rows
	var err error
	if projectID != "" {
		rows, err = d.db.Query(
			`SELECT `+cols+` FROM feedbacks WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	} else {
		rows, err = d.db.Query(
			`SELECT ` + cols + ` FROM feedbacks ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt int64
		var isDuplicate int
		var isPublic int
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.Title, &f.Description, &f.CustomData, &f.FilePaths, &f.ClientIP, &f.Status, &f.Tags, &f.Assignee, &f.ContactName, &f.ContactEmail, &f.TrackingToken, &f.Priority, &isDuplicate, &f.DuplicateOf, &f.Category, &createdAt, &f.UpdatedAt, &f.ContentHash, &isPublic, &f.RoadmapStatus); err != nil {
			return nil, err
		}
		f.IsDuplicate = isDuplicate == 1
		f.PublicOnRoadmap = isPublic == 1
		f.CreatedAt = time.Unix(createdAt, 0)
		list = append(list, f)
	}

	// Batch-load vote counts
	if len(list) > 0 {
		ids := make([]int64, len(list))
		ph := make([]string, len(list))
		args := make([]interface{}, len(list))
		for i, f := range list {
			ids[i] = f.ID
			ph[i] = "?"
			args[i] = f.ID
		}
		vrows, verr := d.db.Query(`SELECT feedback_id, COUNT(*) FROM feedback_votes WHERE feedback_id IN (`+strings.Join(ph, ",")+`) GROUP BY feedback_id`, args...)
		if verr == nil {
			defer vrows.Close()
			vmap := make(map[int64]int, len(list))
			for vrows.Next() {
				var vid int64
				var n int
				if vrows.Scan(&vid, &n) == nil {
					vmap[vid] = n
				}
			}
			for i := range list {
				list[i].Votes = vmap[list[i].ID]
			}
		}

		// Batch-load notes content
		nrows, nerr := d.db.Query(`SELECT feedback_id, content FROM feedback_notes WHERE feedback_id IN (`+strings.Join(ph, ",")+`) ORDER BY feedback_id, created_at`, args...)
		if nerr == nil {
			defer nrows.Close()
			nmap := make(map[int64][]string, len(list))
			for nrows.Next() {
				var fid int64
				var content string
				if nrows.Scan(&fid, &content) == nil {
					nmap[fid] = append(nmap[fid], content)
				}
			}
			for i := range list {
				if notes, ok := nmap[list[i].ID]; ok && len(notes) > 0 {
					list[i].NotesContent = strings.Join(notes, "\n---\n")
				}
			}
		}

		// Batch-load CSAT ratings
		rrows, rerr := d.db.Query(`SELECT feedback_id, score FROM feedback_ratings WHERE feedback_id IN (`+strings.Join(ph, ",")+`)`, args...)
		if rerr == nil {
			defer rrows.Close()
			rmap := make(map[int64]int, len(list))
			for rrows.Next() {
				var fid int64
				var score int
				if rrows.Scan(&fid, &score) == nil {
					rmap[fid] = score
				}
			}
			for i := range list {
				list[i].RatingScore = rmap[list[i].ID]
			}
		}
	}

	return list, nil
}

// UpdateFeedbackAssignee updates the assignee field of a feedback.
func (d *Database) UpdateFeedbackAssignee(id int64, assignee string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET assignee = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, assignee, id)
	return err
}

// UpdateFeedbackPriority updates the priority field of a feedback.
func (d *Database) UpdateFeedbackPriority(id int64, priority string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE feedbacks SET priority = ?, updated_at = strftime('%s', 'now') WHERE id = ?`, priority, id)
	return err
}
