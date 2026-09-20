package web

import (
	"net/http"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// handleEnrolPage serves the first-run setup page. The token comes from
// the URL labpower enrol printed to the console (?token=...).
func (s *Server) handleEnrolPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if err := auth.ValidateEnrolToken(r.Context(), s.Store, token, s.Clock.Now()); err != nil {
		http.Error(w, "invalid or expired enrolment link", http.StatusForbidden)
		return
	}
	s.render(w, "enrol", pageData{Title: "Set up labpower", Data: token})
}

func (s *Server) handleEnrolPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Enrol-Token")
	if err := auth.ValidateEnrolToken(r.Context(), s.Store, token, s.Clock.Now()); err != nil {
		http.Error(w, "invalid or expired enrolment link", http.StatusForbidden)
		return
	}

	wu := auth.WebAuthnUser{ID: store.SoleUserID, Name: "owner"}
	creation, session, err := s.WebAuthn.BeginRegistration(wu)
	if err != nil {
		s.Logger.Error("web: begin enrolment registration", "error", err)
		http.Error(w, "could not start enrolment", http.StatusInternalServerError)
		return
	}
	id := s.newChallengeID()
	s.Challenges.Put(id, *session, s.Clock.Now(), challengeTTL)
	writeJSON(w, map[string]any{"challengeId": id, "publicKey": creation.Response})
}

func (s *Server) handleEnrolPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Enrol-Token")
	now := s.Clock.Now()
	if err := auth.ValidateEnrolToken(r.Context(), s.Store, token, now); err != nil {
		http.Error(w, "invalid or expired enrolment link", http.StatusForbidden)
		return
	}

	id := r.Header.Get("X-Challenge-Id")
	session, ok := s.Challenges.Take(id, now)
	if !ok {
		http.Error(w, "enrolment attempt expired, please try again", http.StatusBadRequest)
		return
	}

	wu := auth.WebAuthnUser{ID: store.SoleUserID, Name: "owner"}
	cred, err := s.WebAuthn.FinishRegistration(wu, session, r)
	if err != nil {
		http.Error(w, "passkey registration failed", http.StatusUnauthorized)
		return
	}

	if err := s.Store.CreateUser(r.Context(), store.User{ID: store.SoleUserID, Name: "owner"}); err != nil {
		s.Logger.Error("web: create user during enrolment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.createCredential(r, store.SoleUserID, *cred); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := auth.ConsumeEnrolToken(r.Context(), s.Store); err != nil {
		s.Logger.Error("web: consume enrol token", "error", err)
	}

	s.finishLogin(w, r, store.SoleUserID)
}

func (s *Server) createCredential(r *http.Request, userID string, cred webauthn.Credential) error {
	data, err := auth.EncodeCredential(cred)
	if err != nil {
		return err
	}
	return s.Store.CreateCredential(r.Context(), store.Credential{
		ID: cred.ID, UserID: userID, Data: data, CreatedAt: s.Clock.Now(),
	})
}
