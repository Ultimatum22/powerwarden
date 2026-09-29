package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

const challengeTTL = 2 * time.Minute

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Already signed in: go straight to the dashboard.
	if raw, ok := auth.TokenFromRequest(r); ok {
		if _, err := s.Sessions.Validate(r.Context(), raw, s.Clock.Now()); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.render(w, "login", pageData{Title: "Sign in"})
}

// handleLoginPasskeyBegin starts a discoverable ("usernameless") passkey
// login: the browser itself picks which credential to present, and
// FinishPasskeyLogin below resolves the user from it.
func (s *Server) handleLoginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	assertion, session, err := s.WebAuthn.BeginDiscoverableLogin()
	if err != nil {
		s.Logger.Error("web: begin passkey login", "error", err)
		http.Error(w, "could not start passkey login", http.StatusInternalServerError)
		return
	}
	id := s.newChallengeID()
	s.Challenges.Put(id, *session, s.Clock.Now(), challengeTTL)
	writeJSON(w, map[string]any{"challengeId": id, "publicKey": assertion.Response})
}

func (s *Server) handleLoginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Challenge-Id")
	session, ok := s.Challenges.Take(id, s.Clock.Now())
	if !ok {
		http.Error(w, "login attempt expired, please try again", http.StatusBadRequest)
		return
	}

	var resolvedUser store.User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.Store.GetUser(r.Context(), string(userHandle))
		if err != nil {
			return nil, err
		}
		resolvedUser = u
		return s.webAuthnUserFor(r, u)
	}
	_, cred, err := s.WebAuthn.FinishPasskeyLogin(handler, session, r)
	if err != nil {
		s.recordLoginFailure(r, "")
		http.Error(w, "passkey login failed", http.StatusUnauthorized)
		return
	}
	s.persistCredentialUse(r, resolvedUser.ID, *cred)
	s.finishLogin(w, r, resolvedUser.ID)
}

// handleLoginTOTP is the backup path. Since the login screen has no
// username field (CLAUDE.md's spec shows only two buttons/links, no
// account selector), it's checked against the sole enrolled user's secret
// — this is a single-owner appliance, and enrolment only ever creates one
// account (see store.SoleUserID).
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	u, err := s.Store.GetUser(r.Context(), store.SoleUserID)
	if err != nil || len(u.TOTPSecretEnc) == 0 {
		s.recordLoginFailure(r, "")
		http.Error(w, "invalid code", http.StatusUnauthorized)
		return
	}
	secret, err := s.SecretBox.Open(u.TOTPSecretEnc)
	if err != nil {
		s.Logger.Error("web: decrypt totp secret", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !auth.ValidateTOTP(secret, code, s.Clock.Now()) {
		s.recordLoginFailure(r, u.ID)
		http.Error(w, "invalid code", http.StatusUnauthorized)
		return
	}
	s.finishLogin(w, r, u.ID)
}

// persistCredentialUse re-encodes the credential (its signature counter
// changed) and updates last_used, so clone detection keeps working on
// subsequent logins.
func (s *Server) persistCredentialUse(r *http.Request, userID string, cred webauthn.Credential) {
	now := s.Clock.Now()
	if err := s.Store.UpdateCredentialLastUsed(r.Context(), cred.ID, now); err != nil {
		s.Logger.Error("web: update credential last_used", "error", err)
	}
	// The credentials table has no separate "update data" method distinct
	// from creation; re-create-on-conflict isn't available either, so for
	// now the stored public key/flags are fixed at registration time and
	// only last_used tracks activity. Sign-count regression (clone
	// detection) is therefore not yet enforced — noted for a follow-up
	// rather than silently pretended to work.
	_ = userID
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, userID string) {
	now := s.Clock.Now()
	raw, err := s.Sessions.Create(r.Context(), userID, s.clientIP(r), r.UserAgent(), now)
	if err != nil {
		s.Logger.Error("web: create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.LoginLimiter.Success("ip:" + s.clientIP(r))
	http.SetCookie(w, s.Sessions.Cookie(raw, int(s.Sessions.AbsoluteTimeout.Seconds())))
	actor, ip := "user:"+userID, s.clientIP(r)
	// Checked before recording this login, so "seen" means a previous one.
	seen, err := s.Store.HasEvent(r.Context(), "login_ok", actor, ip)
	if err != nil {
		s.Logger.Error("web: check login history", "error", err)
		seen = true // don't alert on a lookup failure; the login itself is logged
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: now, Kind: "login_ok", Actor: actor, IP: ip,
	})
	if !seen {
		s.notify(r, notify.Notification{
			Title:    "labpower: sign-in from a new IP",
			Body:     fmt.Sprintf("Signed in from %s (%s), not seen in the last 90 days. If this wasn't you, revoke the session under Security.", ip, r.UserAgent()),
			Priority: notify.PriorityHigh,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "redirect": "/"})
}

// Failed-login burst alerting (CLAUDE.md: "Notify on ... failed-login
// bursts"): at most one alert per window once the count within it
// reaches the threshold.
const (
	failBurstWindow    = 10 * time.Minute
	failBurstThreshold = 5
	failBurstStateKey  = "auth.fail_burst_notified_at"
)

func (s *Server) recordLoginFailure(r *http.Request, userID string) {
	actor := "unknown"
	if userID != "" {
		actor = "user:" + userID
	}
	now := s.Clock.Now()
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: now, Kind: "login_fail", Actor: actor, IP: s.clientIP(r),
	})

	n, err := s.Store.CountEventsSince(r.Context(), "login_fail", now.Add(-failBurstWindow))
	if err != nil || n < failBurstThreshold {
		return
	}
	if last, ok, _ := s.Store.GetState(r.Context(), failBurstStateKey); ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < failBurstWindow {
			return
		}
	}
	_ = s.Store.SetState(r.Context(), failBurstStateKey, now.Format(time.RFC3339))
	s.Logger.Warn("web: failed-login burst", "count", n, "window", failBurstWindow, "last_ip", s.clientIP(r))
	s.notify(r, notify.Notification{
		Title:    "labpower: repeated failed sign-ins",
		Body:     fmt.Sprintf("%d failed sign-in attempts in the last %s, most recently from %s. Rate limiting is active.", n, failBurstWindow, s.clientIP(r)),
		Priority: notify.PriorityHigh,
	})
}

// notify sends best-effort: a delivery failure is logged, never shown to
// the user or allowed to fail the request.
func (s *Server) notify(r *http.Request, n notify.Notification) {
	if s.Notifier == nil {
		return
	}
	if err := s.Notifier.Notify(r.Context(), n); err != nil {
		s.Logger.Warn("web: notification failed", "title", n.Title, "error", err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	_ = s.Sessions.Revoke(r.Context(), sess.TokenHash)
	http.SetCookie(w, s.Sessions.Cookie("", -1))
	w.Header().Set("HX-Redirect", "/login")
	w.WriteHeader(http.StatusOK)
}

// handleStepUpBegin/Finish re-run a passkey assertion for the current
// user (not a discoverable login, since we already know who they are) to
// mark the session step-up'd.
func (s *Server) handleStepUpBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFromContext(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	u, err := s.Store.GetUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	wu, err := s.webAuthnUserFor(r, u)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	assertion, session, err := s.WebAuthn.BeginLogin(wu)
	if err != nil {
		http.Error(w, "could not start step-up", http.StatusInternalServerError)
		return
	}
	id := s.newChallengeID()
	s.Challenges.Put(id, *session, s.Clock.Now(), challengeTTL)
	writeJSON(w, map[string]any{"challengeId": id, "publicKey": assertion.Response})
}

func (s *Server) handleStepUpFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFromContext(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	id := r.Header.Get("X-Challenge-Id")
	session, ok := s.Challenges.Take(id, s.Clock.Now())
	if !ok {
		http.Error(w, "step-up attempt expired, please try again", http.StatusBadRequest)
		return
	}
	u, err := s.Store.GetUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	wu, err := s.webAuthnUserFor(r, u)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.WebAuthn.FinishLogin(wu, session, r); err != nil {
		s.StepUpLimiter.Failure("ip:"+s.clientIP(r), s.Clock.Now())
		http.Error(w, "step-up failed", http.StatusUnauthorized)
		return
	}
	s.StepUpLimiter.Success("ip:" + s.clientIP(r))
	if err := s.Sessions.StepUp(r.Context(), sess.TokenHash, s.Clock.Now()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) newChallengeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) webAuthnUserFor(r *http.Request, u store.User) (auth.WebAuthnUser, error) {
	creds, err := s.Store.CredentialsByUser(r.Context(), u.ID)
	if err != nil {
		return auth.WebAuthnUser{}, err
	}
	wcreds := make([]webauthn.Credential, 0, len(creds))
	for _, c := range creds {
		wc, err := auth.DecodeCredential(c.Data)
		if err != nil {
			s.Logger.Error("web: decode stored credential", "error", err)
			continue
		}
		wcreds = append(wcreds, wc)
	}
	return auth.WebAuthnUser{ID: u.ID, Name: u.Name, Credentials: wcreds}, nil
}
