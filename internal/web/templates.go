package web

import (
	"fmt"
	"html/template"
	"net/http"

	assets "github.com/Ultimatum22/powerwarden/web"
)

var funcMap = template.FuncMap{
	"pct": func(v float64) string { return fmt.Sprintf("%.2f%%", v) },
}

// pages maps a page name (e.g. "dashboard") to its fully-parsed template:
// the shared layout/nav plus that page's own "content" definition. Each
// page gets its own clone of the base because html/template can't select
// a "content" template dynamically by name — every page defining a
// template literally named "content" in a shared set would just overwrite
// each other.
var pages = mustParsePages(
	"dashboard", "timeline", "vacation", "events", "security",
	"login", "enrol", "host_shutdown",
)

// fragments are htmx partial responses with no layout wrapping.
var fragments = template.Must(template.New("").Funcs(funcMap).ParseFS(assets.Templates,
	"templates/partial_host.html", "templates/partial_weather.html", "templates/partial_guests.html",
))

func mustParsePages(names ...string) map[string]*template.Template {
	out := make(map[string]*template.Template, len(names))
	base := template.Must(template.New("").Funcs(funcMap).ParseFS(assets.Templates, "templates/layout.html", "templates/icons.html"))
	for _, name := range names {
		clone := template.Must(base.Clone())
		out[name] = template.Must(clone.ParseFS(assets.Templates, "templates/"+name+".html"))
	}
	return out
}

// pageData is passed to every page template; the layout renders the
// nav/session box from it, and Data carries page-specific content.
type pageData struct {
	Title      string
	ActiveNav  string // "dashboard" | "timeline" | "vacation" | "events" | "security"
	CSRFToken  string
	Vacation   bool
	SessionEnd string // human-readable, for the desktop sidebar session box
	Data       any
}

// standalonePages render their own complete <html> document (entry point
// template named after the page itself) instead of the authenticated
// app-shell layout — login and enrolment happen before there's a session
// to show a nav/sidebar for.
var standalonePages = map[string]bool{"login": true, "enrol": true}

func (s *Server) render(w http.ResponseWriter, name string, pd pageData) {
	tmpl, ok := pages[name]
	if !ok {
		s.Logger.Error("web: unknown page template", "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	entryPoint := "layout"
	if standalonePages[name] {
		entryPoint = name
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, entryPoint, pd); err != nil {
		s.Logger.Error("web: render page failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := fragments.ExecuteTemplate(w, name, data); err != nil {
		s.Logger.Error("web: render fragment failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
