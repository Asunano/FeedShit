package routes

import (
	"bytes"
	"embed"
	"html"
	"html/template"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// adminTabDefs lists every admin partial (a tab pane or the shared-modals block),
// its file under frontend/pages/admin/ and the {{define "..."}} name it registers.
// Each entry is parsed INDEPENDENTLY so a syntax error in one partial is isolated
// to that module and cannot take down the rest of the admin UI.
var adminTabDefs = []struct {
	file string // file name under frontend/pages/admin/
	name string // {{define "..."}} name referenced by the shell via renderTab
}{
	{"dashboard.html", "adminDashboard"},
	{"pending.html", "adminPending"},
	{"roadmap.html", "adminRoadmap"},
	{"projects.html", "adminProjects"},
	{"audit.html", "adminAudit"},
	{"team.html", "adminTeam"},
	{"settings.html", "adminSettings"},
	{"kb.html", "adminKb"},
	{"feedback-detail.html", "adminFeedbackDetail"},
}

// adminTemplateSet holds the admin shell plus each tab parsed as an independent
// template. A tab failing to parse is recorded in failedTabErrors but does not
// prevent the shell or the other tabs from rendering — this is what makes a
// single broken module non-fatal for the rest of the admin UI.
type adminTemplateSet struct {
	shell          *template.Template
	tabs           map[string]*template.Template
	failedTabErrors map[string]error
}

// loadAdminTemplates builds the isolated admin template set:
//  1. every tab partial is parsed on its own; a parse failure is isolated
//  2. the shell (admin.html + base.html) is parsed once with a renderTab func
//     that resolves each tab from the set at execute time
//
// The shell is returned even when some tabs failed; only a shell-parse failure
// leaves set.shell == nil (admin UI fully disabled with a friendly error).
func loadAdminTemplates(fsys embed.FS) *adminTemplateSet {
	set := &adminTemplateSet{
		tabs:            make(map[string]*template.Template),
		failedTabErrors: make(map[string]error),
	}

	// 1) Load each tab independently.
	for _, d := range adminTabDefs {
		raw, err := fsys.ReadFile("frontend/pages/admin/" + d.file)
		if err != nil {
			set.failedTabErrors[d.name] = err
			log.Printf("WARN: admin tab %q file unreadable (isolated): %v", d.name, err)
			continue
		}
		t, err := template.New(d.name).Parse(string(raw))
		if err != nil {
			set.failedTabErrors[d.name] = err
			log.Printf("WARN: admin tab %q failed to parse (isolated, other tabs unaffected): %v", d.name, err)
			continue
		}
		set.tabs[d.name] = t
	}

	// 2) Build the renderTab func that resolves a tab from the set. Because the
	//    func returns template.HTML, the per-tab fragment is already context-
	//    escaped and inserted trusted — no double-escaping, no cascade on error.
	funcs := template.FuncMap{
		"renderTab": func(name string, data interface{}) template.HTML {
			return set.renderTab(name, data)
		},
	}

	// 3) Parse the shell (references renderTab + chrometop/chromebot from base.html).
	shell, err := template.New("admin").Funcs(funcs).ParseFS(fsys,
		"frontend/layouts/base.html",
		"frontend/pages/admin.html")
	if err != nil {
		log.Printf("WARN: admin shell failed to parse (admin UI disabled until fixed): %v", err)
		return set // shell stays nil
	}
	set.shell = shell

	if len(set.failedTabErrors) > 0 {
		log.Printf("WARN: %d admin tab(s) failed to load and will render isolated error boxes: %v",
			len(set.failedTabErrors), keysOf(set.failedTabErrors))
	}
	return set
}

// renderTab executes a single admin tab/partial and returns its already-escaped
// HTML. On any failure (missing or broken tab) it returns an isolated error box
// instead of propagating the error up to the shell.
func (s *adminTemplateSet) renderTab(name string, data interface{}) template.HTML {
	t, ok := s.tabs[name]
	if !ok {
		return template.HTML(`<div class="admin-tab-error" role="alert">⚠️ 该模块（<code>` + html.EscapeString(name) + `</code>）加载失败，已隔离。其他模块不受影响。</div>`)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("WARN: render admin tab %q failed (isolated): %v", name, err)
		return template.HTML(`<div class="admin-tab-error" role="alert">⚠️ 该模块（<code>` + html.EscapeString(name) + `</code>）渲染出错，已隔离。其他模块不受影响。</div>`)
	}
	return template.HTML(buf.String())
}

// serveAdmin renders the admin shell (each tab isolated via renderTab).
func serveAdmin(c *gin.Context, set *adminTemplateSet) {
	if set == nil || set.shell == nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("后台模板解析失败，请检查后台模板文件。"))
		return
	}
	var buf bytes.Buffer
	if err := set.shell.ExecuteTemplate(&buf, "admin.html", PageData{Nav: "admin", Nonce: nonceOf(c)}); err != nil {
		c.Data(http.StatusInternalServerError, "text/plain; charset=utf-8", []byte("template render error: "+err.Error()))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

// keysOf returns the keys of a map for logging purposes.
func keysOf(m map[string]error) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
