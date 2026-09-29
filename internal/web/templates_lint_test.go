package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	assets "github.com/Ultimatum22/powerwarden/web"
)

// The CSP (see cspHeader) allows no inline styles or scripts, so anything
// below would be silently blocked by the browser. Catch it at test time
// instead of in the console.
var cspLintRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"inline style attribute", regexp.MustCompile(`(?i)\sstyle\s*=`)},
	{"<style> element", regexp.MustCompile(`(?i)<style[\s>]`)},
	{"inline event handler", regexp.MustCompile(`(?i)\son[a-z]+\s*=`)},
	{"hx-on handler (needs eval)", regexp.MustCompile(`(?i)\shx-on[:-]`)},
	{"javascript: URL", regexp.MustCompile(`(?i)javascript:`)},
}

var scriptTag = regexp.MustCompile(`(?is)<script\b[^>]*>`)

func templateSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(assets.Templates, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(assets.Templates, path)
		out[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no templates found")
	}
	return out
}

func TestTemplatesAreCSPClean(t *testing.T) {
	for path, src := range templateSources(t) {
		for _, rule := range cspLintRules {
			if loc := rule.re.FindStringIndex(src); loc != nil {
				t.Errorf("%s: %s at %q", path, rule.name, excerpt(src, loc[0]))
			}
		}
		for _, tag := range scriptTag.FindAllString(src, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s: inline <script> without src: %q", path, tag)
			}
		}
	}
}

// stepUpRoutes are the POST routes wrapped in requireStepUp (routes.go).
// Any htmx form posting to one must carry data-stepup, so app.js runs the
// passkey assertion first; otherwise the request just gets a 403.
var stepUpRoutes = []string{"/host/shutdown", "/vacation", "/vacation/end", "/weather/ignore"}

var hxPostTag = regexp.MustCompile(`(?is)<[a-z]+\b[^>]*\bhx-post="([^"]*)"[^>]*>`)

func TestStepUpFormsAreMarked(t *testing.T) {
	protected := map[string]bool{}
	for _, r := range stepUpRoutes {
		protected[r] = true
	}
	found := map[string]bool{}
	for path, src := range templateSources(t) {
		for _, m := range hxPostTag.FindAllStringSubmatch(src, -1) {
			tag, target := m[0], m[1]
			if !protected[target] {
				continue
			}
			found[target] = true
			if !strings.Contains(tag, "data-stepup") {
				t.Errorf("%s: %q posts to step-up route %s without data-stepup", path, tag, target)
			}
		}
	}
	for _, r := range stepUpRoutes {
		if !found[r] {
			t.Errorf("no template posts to step-up route %s; update stepUpRoutes if it was removed", r)
		}
	}
}

func excerpt(s string, i int) string {
	end := min(i+60, len(s))
	return strings.TrimSpace(s[i:end])
}
