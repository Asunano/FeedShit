package email

import (
	"strings"
	"testing"

	"feedshit/internal/database"
)

func TestRenderTemplateSubstitutes(t *testing.T) {
	out := renderTemplate("Hello {{title}} from {{project}}", map[string]string{
		"title":   "Bug",
		"project": "acme",
	})
	if !strings.Contains(out, "Hello Bug from acme") {
		t.Fatalf("unexpected render output: %q", out)
	}
}

func TestRenderTemplateEscapesUserInput(t *testing.T) {
	out := renderTemplate("{{description}}", map[string]string{
		"description": "<script>alert(1)</script>",
	})
	if strings.Contains(out, "<script>") {
		t.Fatalf("user input was not HTML-escaped: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag, got: %q", out)
	}
}

func TestRenderTemplateKeepsURLFields(t *testing.T) {
	url := "http://example.com/x?y=1"
	out := renderTemplate("{{admin_url}}", map[string]string{
		"admin_url": url,
	})
	if !strings.Contains(out, url) {
		t.Fatalf("URL field should not be escaped, got: %q", out)
	}
}

func TestBuildNotificationSubjectDefault(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	subj := BuildNotificationSubject(db, map[string]string{
		"title":   "Crash on boot",
		"project": "acme",
	})
	if !strings.Contains(subj, "Crash on boot") || !strings.Contains(subj, "acme") {
		t.Fatalf("default subject missing fields: %q", subj)
	}
}

func TestBuildNotificationSubjectCustomTemplate(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	if err := db.SetConfig("email_template_subject", "CUSTOM:{{title}}", "subject tpl"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	subj := BuildNotificationSubject(db, map[string]string{
		"title":   "MyTitle",
		"project": "acme",
	})
	if subj != "CUSTOM:MyTitle" {
		t.Fatalf("custom subject template not applied: %q", subj)
	}
}

func TestBuildNotificationBodyDefault(t *testing.T) {
	db, err := database.NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	body := BuildNotificationBody(db, map[string]string{
		"title":     "Crash on boot",
		"project":   "acme",
		"client_ip": "1.2.3.4",
	})
	if !strings.Contains(body, "Crash on boot") {
		t.Fatalf("default body missing title: %q", body)
	}
}
