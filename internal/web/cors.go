package web

import "net/http"

// newCrossOriginProtection builds Go 1.25's CSRF guard, trusting only the
// configured public origin (CLAUDE.md: "http.CrossOriginProtection on all
// state-changing routes"). If publicOrigin is empty (e.g. in tests
// exercising same-origin requests only), no trusted origin is added and
// same-origin requests are still allowed by the library's default
// behavior.
func newCrossOriginProtection(publicOrigin string) *http.CrossOriginProtection {
	cop := http.NewCrossOriginProtection()
	if publicOrigin != "" {
		_ = cop.AddTrustedOrigin(publicOrigin)
	}
	return cop
}
