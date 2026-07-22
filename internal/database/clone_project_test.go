package database

import "testing"

// TestCloneProject verifies that CloneProject copies the form schema, description,
// announcement and categories of the source, resets the clone to active +
// unarchived, and does NOT copy feedbacks.
func TestCloneProject(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}

	src := &Project{
		Name:         "源项目",
		Slug:         "source",
		Description:  "原始描述",
		IsActive:     false, // source is inactive/archived to prove the clone resets these
		IsArchived:   true,
		FormSchema:   `[{"key":"title","type":"text"}]`,
		Announcement: "注意",
	}
	srcID, err := db.CreateProject(src)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Seed two categories on the source project.
	if _, err := db.CreateCategory("source", "bug", "缺陷", "#ff0000", 1); err != nil {
		t.Fatalf("CreateCategory bug: %v", err)
	}
	if _, err := db.CreateCategory("source", "idea", "建议", "#00ff00", 2); err != nil {
		t.Fatalf("CreateCategory idea: %v", err)
	}

	newID, err := db.CloneProject(srcID, "源项目 (副本)", "source-copy")
	if err != nil {
		t.Fatalf("CloneProject: %v", err)
	}
	if newID == srcID {
		t.Fatalf("clone should have a new ID, got %d (== src)", newID)
	}

	clone, err := db.GetProjectBySlug("source-copy")
	if err != nil {
		t.Fatalf("GetProjectBySlug clone: %v", err)
	}
	if clone == nil {
		t.Fatalf("clone project not found by slug")
	}
	if clone.Name != "源项目 (副本)" {
		t.Errorf("name: want %q got %q", "源项目 (副本)", clone.Name)
	}
	if clone.Description != "原始描述" {
		t.Errorf("description: want %q got %q", "原始描述", clone.Description)
	}
	if clone.Announcement != "注意" {
		t.Errorf("announcement: want %q got %q", "注意", clone.Announcement)
	}
	if clone.FormSchema != `[{"key":"title","type":"text"}]` {
		t.Errorf("form_schema not copied: %q", clone.FormSchema)
	}
	if !clone.IsActive {
		t.Errorf("clone should be active, got is_active=%v", clone.IsActive)
	}
	if clone.IsArchived {
		t.Errorf("clone should NOT be archived, got is_archived=%v", clone.IsArchived)
	}

	cloneCats, err := db.ListCategories("source-copy")
	if err != nil {
		t.Fatalf("ListCategories clone: %v", err)
	}
	if len(cloneCats) != 2 {
		t.Fatalf("expected 2 cloned categories, got %d", len(cloneCats))
	}
	got := map[string]string{}
	for _, c := range cloneCats {
		got[c.Key] = c.Name
		if c.ProjectSlug != "source-copy" {
			t.Errorf("category slug mismatch: want source-copy got %q", c.ProjectSlug)
		}
	}
	if got["bug"] != "缺陷" || got["idea"] != "建议" {
		t.Errorf("cloned category names wrong: %v", got)
	}

	// Feedback count must be zero on a fresh clone.
	if clone.FeedbackCount != 0 {
		t.Errorf("clone should have 0 feedbacks, got %d", clone.FeedbackCount)
	}
}
