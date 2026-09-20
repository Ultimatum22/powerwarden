package web

import "net/http"

// newCrossOriginProtection builds Go 1.25's CSRF guard, trusting only the
// configured public hostname's origin (CLAUDE.md: "http.CrossOriginProtection
// on all state-changing routes"). If publicHostname is empty (e.g. in
// tests exercising same-origin requests only), no trusted origin is added
// and same-origin requests are still allowed by the library's default
// behavior.
func newCrossOriginProtection(publicHostname string) *http.CrossOriginProtection {
	cop := http.NewCrossOriginProtection()
	if publicHostname != "" {
		_ = cop.AddTrustedOrigin("https://" + publicHostname)
	}
	return cop
}
