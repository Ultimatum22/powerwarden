package web

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"image/png"
	"net/http"
	"net/url"
	"regexp"
	"sort"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// registrationOptions require a discoverable credential: sign-in is
// usernameless (BeginDiscoverableLogin), so a non-resident credential,
// e.g. on a hardware key that defaults to non-resident, could never be
// used to log in.
func registrationOptions(existing []webauthn.Credential) []webauthn.RegistrationOption {
	exclude := make([]protocol.CredentialDescriptor, 0, len(existing))
	for i := range existing {
		exclude = append(exclude, existing[i].Descriptor())
	}
	return []webauthn.RegistrationOption{
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude),
	}
}

// securityChanged records a security-settings change and notifies, since
// an attacker with a session would do exactly these things first.
func (s *Server) securityChanged(r *http.Request, kind, what string) {
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: s.Clock.Now(), Kind: kind, Actor: s.actor(r), IP: s.clientIP(r),
	})
	s.notify(r, notify.Notification{
		Title:    "labpower: security settings changed",
		Body:     fmt.Sprintf("%s by %s from %s.", what, s.actor(r), s.clientIP(r)),
		Priority: notify.PriorityHigh,
	})
}

// handlePasskeyAddBegin/Finish register an additional passkey for the
// signed-in user (step-up required).
func (s *Server) handlePasskeyAddBegin(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
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
	creation, session, err := s.WebAuthn.BeginRegistration(wu, registrationOptions(wu.Credentials)...)
	if err != nil {
		s.Logger.Error("web: begin passkey registration", "error", err)
		http.Error(w, "could not start registration", http.StatusInternalServerError)
		return
	}
	id := s.newChallengeID()
	s.Challenges.Put(id, *session, s.Clock.Now(), challengeTTL)
	writeJSON(w, map[string]any{"challengeId": id, "publicKey": creation.Response})
}

func (s *Server) handlePasskeyAddFinish(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	session, ok := s.Challenges.Take(r.Header.Get("X-Challenge-Id"), s.Clock.Now())
	if !ok {
		http.Error(w, "registration attempt expired, please try again", http.StatusBadRequest)
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
	cred, err := s.WebAuthn.FinishRegistration(wu, session, r)
	if err != nil {
		s.Logger.Warn("web: passkey registration failed", "error", err)
		http.Error(w, "passkey registration failed", http.StatusBadRequest)
		return
	}
	if err := s.createCredential(r, sess.UserID, *cred); err != nil {
		s.Logger.Error("web: store new passkey", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.securityChanged(r, "passkey_added", "A passkey was added")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	id, err := hex.DecodeString(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	creds, err := s.Store.CredentialsByUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	found := false
	for _, c := range creds {
		found = found || bytes.Equal(c.ID, id)
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Passkeys are the primary login and there is no password login, so
	// removing the last one would lock the owner out (TOTP is a backup,
	// not a replacement).
	if len(creds) == 1 {
		http.Error(w, "cannot remove the last passkey", http.StatusConflict)
		return
	}
	if err := s.Store.DeleteCredential(r.Context(), id, sess.UserID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.securityChanged(r, "passkey_removed", "A passkey was removed")
	s.hxRefresh(w, r)
}

// handleTOTPSetup generates a new secret and returns the setup fragment
// (QR code + confirmation form). Nothing is stored until the owner proves
// their app produces valid codes (handleTOTPConfirm).
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	secret, otpauthURL, err := auth.GenerateTOTPSecret("labpower", "owner")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	qr, err := qrDataURL(otpauthURL)
	if err != nil {
		s.Logger.Error("web: render totp qr", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderFragment(w, "partial_totp_setup", totpSetupView{Secret: secret, QR: qr})
}

type totpSetupView struct {
	Secret string
	QR     template.URL
	Error  string
}

// qrDataURL renders the otpauth:// URL as a PNG data URL (the CSP allows
// img-src data:). template.URL is safe here: the content is a PNG the
// server just generated, not user input.
func qrDataURL(otpauthURL string) (template.URL, error) {
	key, err := otp.NewKeyFromURL(otpauthURL)
	if err != nil {
		return "", err
	}
	img, err := key.Image(200, 200)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secret, code := r.PostForm.Get("secret"), r.PostForm.Get("code")
	if !validTOTPSecret.MatchString(secret) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !auth.ValidateTOTP(secret, code, s.Clock.Now()) {
		qr, _ := qrDataURLForSecret(secret)
		w.Header().Set("Cache-Control", "no-store")
		s.renderFragment(w, "partial_totp_setup", totpSetupView{Secret: secret, QR: qr, Error: "That code didn't match. Check the time on your phone and try the current code."})
		return
	}
	enc, err := s.SecretBox.Seal(secret)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetUserTOTPSecret(r.Context(), sess.UserID, enc); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.securityChanged(r, "totp_enabled", "An authenticator app was set up")
	s.hxRefresh(w, r)
}

// validTOTPSecret accepts only an unpadded base32 secret of a sane
// length: the confirm form round-trips the secret through the browser.
var validTOTPSecret = regexp.MustCompile(`^[A-Z2-7]{16,128}$`)

// qrDataURLForSecret re-renders the QR code for a secret being confirmed,
// so a mistyped code can show the same code again.
func qrDataURLForSecret(secret string) (template.URL, error) {
	v := url.Values{"issuer": {"labpower"}, "secret": {secret}}
	return qrDataURL("otpauth://totp/labpower:owner?" + v.Encode())
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	if err := s.Store.SetUserTOTPSecret(r.Context(), sess.UserID, nil); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.securityChanged(r, "totp_disabled", "The authenticator app was removed")
	s.hxRefresh(w, r)
}

// handleSessionRevoke ends one of the user's sessions. No step-up: it can
// only reduce access, and should be quick when something looks wrong.
func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	target, err := hex.DecodeString(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sessions, err := s.Store.SessionsByUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	found := false
	for _, sv := range sessions {
		found = found || bytes.Equal(sv.TokenHash, target)
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := s.Sessions.Revoke(r.Context(), target); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: s.Clock.Now(), Kind: "session_revoked", Actor: s.actor(r), IP: s.clientIP(r),
	})
	if bytes.Equal(target, sess.TokenHash) {
		http.SetCookie(w, s.Sessions.Cookie("", -1))
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.hxRefresh(w, r)
}

// recentLoginAttempts merges the latest successful and failed sign-ins.
func (s *Server) recentLoginAttempts(r *http.Request, limit int) ([]store.Event, error) {
	var out []store.Event
	for _, kind := range []string{"login_ok", "login_fail"} {
		evs, err := s.Store.ListEvents(r.Context(), store.EventQuery{Kind: kind, Limit: limit})
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].ID > out[j].ID
		}
		return out[i].At.After(out[j].At)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
