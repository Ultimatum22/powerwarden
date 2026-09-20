package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// cspHeader matches CLAUDE.md's "HTTP hardening" list exactly.
const cspHeader = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// withGlobalMiddleware wraps every response with the required security
// headers and body-size limiting, then CSRF protection (http's built-in
// cross-origin check) on every request. Per-route auth/step-up middleware
// is applied when routes are registered (see routes.go).
func (s *Server) withGlobalMiddleware(next http.Handler) http.Handler {
	cop := newCrossOriginProtection(s.PublicHostname)

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", cspHeader)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "interest-cohort=()")

		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

		next.ServeHTTP(w, r)
	})
	return cop.Handler(wrapped)
}

// noStore marks a handler's responses as never cached, for authenticated
// pages (CLAUDE.md: "Cache-Control: no-store on authenticated pages").
func noStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

// csrfToken derives a per-session CSRF token via HMAC over the session's
// token hash, so no extra storage is needed and the token is automatically
// invalidated along with the session.
func (s *Server) csrfToken(sess store.Session) string {
	mac := hmac.New(sha256.New, s.CSRFKey)
	mac.Write(sess.TokenHash)
	return hex.EncodeToString(mac.Sum(nil))
}

var errCSRFMismatch = errors.New("web: csrf token mismatch")

// requireCSRFHeader checks the per-session token htmx sends as a header
// (CLAUDE.md: "a per-session CSRF token sent by htmx as a header
// (hx-headers), verified server-side") — defense in depth alongside the
// cross-origin check in withGlobalMiddleware.
func (s *Server) requireCSRFHeader(sess store.Session, r *http.Request) error {
	want := s.csrfToken(sess)
	got := r.Header.Get("X-CSRF-Token")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errCSRFMismatch
	}
	return nil
}

// requireSession loads and validates the session cookie, attaching it to
// the request context for downstream handlers. onFail handles the
// missing/expired case (redirect to /login for pages, 401 for fragments).
func (s *Server) requireSession(onFail http.HandlerFunc, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := auth.TokenFromRequest(r)
		if !ok {
			onFail(w, r)
			return
		}
		sess, err := s.Sessions.Validate(r.Context(), raw, s.Clock.Now())
		if err != nil {
			onFail(w, r)
			return
		}
		next(w, r.WithContext(withSession(r.Context(), sess)))
	}
}

// redirectToLogin is the standard onFail for page routes.
func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// unauthorized is the standard onFail for htmx fragment/API routes, which
// shouldn't redirect (htmx would swap the login page into a fragment slot).
func unauthorized(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// requireStepUp wraps a state-changing handler that needs a fresh passkey
// assertion (CLAUDE.md: "host shutdown, vacation mode, ignore-weather, and
// security settings need a fresh passkey assertion"). Must run after
// requireSession.
func (s *Server) requireStepUp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok {
			unauthorized(w, r)
			return
		}
		if !auth.HasStepUp(sess, s.Clock.Now()) {
			http.Error(w, "step-up required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// requireCSRF wraps a state-changing handler with the per-session header
// check. Must run after requireSession.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok {
			unauthorized(w, r)
			return
		}
		if err := s.requireCSRFHeader(sess, r); err != nil {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// rateLimited wraps a handler with a rate limiter keyed by client IP,
// recording failure/success based on statusIndicatesFailure. Used for
// /login* and /stepup* routes (CLAUDE.md: "Rate limiting: per IP and per
// account on login and step-up, with exponential backoff and lockout").
func (s *Server) rateLimited(rl *auth.RateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := "ip:" + s.clientIP(r)
		now := s.Clock.Now()
		if !rl.Allow(key, now) {
			http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		if rec.status >= 400 {
			rl.Failure(key, now)
		} else {
			rl.Success(key)
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
