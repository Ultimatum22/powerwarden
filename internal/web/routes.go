package web

import (
	"net/http"

	assets "github.com/Ultimatum22/powerwarden/web"
)

// routes registers every route from CLAUDE.md's "HTTP routes" table.
func (s *Server) routes() {
	mux := s.mux

	// Static assets (embedded CSS, vendored htmx, fonts).
	mux.Handle("GET /static/", http.FileServerFS(assets.Static))

	// Unauthenticated.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /login", noStore(s.handleLoginPage))
	mux.HandleFunc("POST /login/passkey/begin", s.rateLimited(s.LoginLimiter, s.handleLoginPasskeyBegin))
	mux.HandleFunc("POST /login/passkey/finish", s.rateLimited(s.LoginLimiter, s.handleLoginPasskeyFinish))
	mux.HandleFunc("POST /login/totp", s.rateLimited(s.LoginLimiter, s.handleLoginTOTP))

	// First-run enrolment (see auth.md for why this isn't in CLAUDE.md's
	// literal route table: the token-based bootstrap flow needs somewhere
	// to redeem the token labpower enrol prints).
	mux.HandleFunc("GET /enrol", noStore(s.handleEnrolPage))
	mux.HandleFunc("POST /enrol/passkey/begin", s.rateLimited(s.LoginLimiter, s.handleEnrolPasskeyBegin))
	mux.HandleFunc("POST /enrol/passkey/finish", s.rateLimited(s.LoginLimiter, s.handleEnrolPasskeyFinish))

	// Authenticated pages.
	mux.HandleFunc("GET /", noStore(s.requireSession(redirectToLogin, s.handleDashboard)))
	mux.HandleFunc("GET /timeline", noStore(s.requireSession(redirectToLogin, s.handleTimeline)))
	mux.HandleFunc("GET /vacation", noStore(s.requireSession(redirectToLogin, s.handleVacationPage)))
	mux.HandleFunc("GET /events", noStore(s.requireSession(redirectToLogin, s.handleEvents)))
	mux.HandleFunc("GET /security", noStore(s.requireSession(redirectToLogin, s.handleSecurityPage)))
	mux.HandleFunc("GET /host/shutdown", noStore(s.requireSession(redirectToLogin, s.handleHostShutdownPage)))

	// htmx fragments.
	mux.HandleFunc("GET /partials/host", noStore(s.requireSession(unauthorized, s.handlePartialHost)))
	mux.HandleFunc("GET /partials/weather", noStore(s.requireSession(unauthorized, s.handlePartialWeather)))
	mux.HandleFunc("GET /partials/guests", noStore(s.requireSession(unauthorized, s.handlePartialGuests)))

	// Session-scoped actions (no step-up).
	mux.HandleFunc("POST /logout", s.requireSession(unauthorized, s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("POST /stepup/begin", s.requireSession(unauthorized, s.rateLimited(s.StepUpLimiter, s.handleStepUpBegin)))
	mux.HandleFunc("POST /stepup/finish", s.requireSession(unauthorized, s.rateLimited(s.StepUpLimiter, s.handleStepUpFinish)))
	mux.HandleFunc("POST /guests/{name}/start", s.requireSession(unauthorized, s.requireCSRF(s.handleGuestStart)))
	mux.HandleFunc("POST /guests/{name}/stop", s.requireSession(unauthorized, s.requireCSRF(s.handleGuestStop)))
	mux.HandleFunc("POST /overrides/{id}/cancel", s.requireSession(unauthorized, s.requireCSRF(s.handleOverrideCancel)))
	mux.HandleFunc("POST /host/wake", s.requireSession(unauthorized, s.requireCSRF(s.handleHostWake)))
	mux.HandleFunc("POST /schedules/pause", s.requireSession(unauthorized, s.requireCSRF(s.handleSchedulesPause)))

	// Step-up required.
	mux.HandleFunc("POST /host/shutdown", s.requireSession(unauthorized, s.requireCSRF(s.requireStepUp(s.handleHostShutdown))))
	mux.HandleFunc("POST /vacation", s.requireSession(unauthorized, s.requireCSRF(s.requireStepUp(s.handleVacationStart))))
	mux.HandleFunc("POST /vacation/end", s.requireSession(unauthorized, s.requireCSRF(s.requireStepUp(s.handleVacationEnd))))
	mux.HandleFunc("POST /weather/ignore", s.requireSession(unauthorized, s.requireCSRF(s.requireStepUp(s.handleWeatherIgnore))))
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
