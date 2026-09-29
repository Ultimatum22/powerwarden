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

// stepUpFormRoutes are the requireStepUp routes (routes.go) that htmx
// forms post to. Such a form must carry data-stepup, so app.js runs the
// passkey assertion first; otherwise the request just gets a 403. ({id}
// matches a template action like {{.IDHex}}.) The passkey-registration
// routes are driven by app.js directly and covered by softauthn_test.go.
var stepUpFormRoutes = []string{
	"/host/shutdown", "/vacation", "/vacation/end", "/weather/ignore",
	"/security/passkeys/{id}/delete", "/security/totp/setup", "/security/totp/confirm", "/security/totp/disable",
}

var hxPostTag = regexp.MustCompile(`(?is)<[a-z]+\b[^>]*\bhx-post="([^"]*)"[^>]*>`)

func routeMatcher(route string) *regexp.Regexp {
	parts := strings.Split(route, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") {
			parts[i] = `[^/]+`
		} else {
			parts[i] = regexp.QuoteMeta(p)
		}
	}
	return regexp.MustCompile("^" + strings.Join(parts, "/") + "$")
}

func TestStepUpFormsAreMarked(t *testing.T) {
	found := map[string]bool{}
	for path, src := range templateSources(t) {
		for _, m := range hxPostTag.FindAllStringSubmatch(src, -1) {
			tag, target := m[0], m[1]
			for _, route := range stepUpFormRoutes {
				if !routeMatcher(route).MatchString(target) {
					continue
				}
				found[route] = true
				if !strings.Contains(tag, "data-stepup") {
					t.Errorf("%s: %q posts to step-up route %s without data-stepup", path, tag, route)
				}
			}
		}
	}
	for _, r := range stepUpFormRoutes {
		if !found[r] {
			t.Errorf("no template posts to step-up route %s; update stepUpFormRoutes if it was removed", r)
		}
	}
}

func excerpt(s string, i int) string {
	end := min(i+60, len(s))
	return strings.TrimSpace(s[i:end])
}
