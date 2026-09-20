// Package web is labpower's server-rendered UI: html/template + htmx,
// dark theme, mobile-first, everything embedded in the binary. See
// CLAUDE.md's "UI specification" and "Security requirements" — the latter
// apply from milestone 1 even while the UI is VPN/Pangolin-private.
package web

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/time/rate"

	"github.com/Ultimatum22/powerwarden/internal/auth"
	"github.com/Ultimatum22/powerwarden/internal/clock"
	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/notify"
	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// Timeouts etc. per CLAUDE.md's "HTTP hardening".
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 15 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 60 * time.Second
	MaxHeaderBytes    = 16 * 1024

	// MaxBodyBytes limits request bodies (form posts here are tiny).
	MaxBodyBytes = 64 * 1024
)

// Server holds everything the HTTP handlers need. Construct with New.
type Server struct {
	Store    *store.Store
	Engine   *engine.Engine
	Proxmox  proxmox.Client
	Notifier notify.Notifier
	Logger   *slog.Logger
	Clock    clock.Clock

	Sessions   *auth.Sessions
	WebAuthn   *webauthn.WebAuthn
	Challenges *auth.ChallengeStore
	SecretBox  *auth.SecretBox
	CSRFKey    []byte // HMAC key for per-session CSRF tokens

	LoginLimiter   *auth.RateLimiter
	StepUpLimiter  *auth.RateLimiter
	TrustedProxy   string
	RPID           string
	PublicHostname string // for CSP/origin checks and cookie scoping context

	// Guests/Schedules/Host/Loc mirror engine.Config's scheduling inputs,
	// so pages can describe "what the schedule says" (e.g. "Shuts down at
	// 01:30") without reaching into the engine's internal state — the web
	// layer reads live Proxmox/store state directly and only ever writes
	// overrides, leaving the engine to reconcile them on its own tick.
	Guests    []engine.GuestConfig
	Schedules map[string]schedule.Schedule
	Host      engine.HostConfig
	Loc       *time.Location

	mux *http.ServeMux
}

// Config configures a new Server.
type Config struct {
	Store    *store.Store
	Engine   *engine.Engine
	Proxmox  proxmox.Client
	Notifier notify.Notifier
	Logger   *slog.Logger
	Clock    clock.Clock

	Sessions   *auth.Sessions
	WebAuthn   *webauthn.WebAuthn
	Challenges *auth.ChallengeStore
	SecretBox  *auth.SecretBox
	CSRFKey    []byte

	LoginLimiter  *auth.RateLimiter
	StepUpLimiter *auth.RateLimiter
	TrustedProxy  string
	RPID          string

	PublicHostname string

	Guests    []engine.GuestConfig
	Schedules map[string]schedule.Schedule
	Host      engine.HostConfig
	Loc       *time.Location
}

// New builds a Server and registers all routes.
func New(cfg Config) (*Server, error) {
	if cfg.CSRFKey == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("web: generate csrf key: %w", err)
		}
		cfg.CSRFKey = key
	}
	s := &Server{
		Store:          cfg.Store,
		Engine:         cfg.Engine,
		Proxmox:        cfg.Proxmox,
		Notifier:       cfg.Notifier,
		Logger:         cfg.Logger,
		Clock:          cfg.Clock,
		Sessions:       cfg.Sessions,
		WebAuthn:       cfg.WebAuthn,
		Challenges:     cfg.Challenges,
		SecretBox:      cfg.SecretBox,
		CSRFKey:        cfg.CSRFKey,
		LoginLimiter:   cfg.LoginLimiter,
		StepUpLimiter:  cfg.StepUpLimiter,
		TrustedProxy:   cfg.TrustedProxy,
		RPID:           cfg.RPID,
		PublicHostname: cfg.PublicHostname,
		Guests:         cfg.Guests,
		Schedules:      cfg.Schedules,
		Host:           cfg.Host,
		Loc:            cfg.Loc,
	}
	if s.Logger == nil {
		s.Logger = slog.New(slog.DiscardHandler)
	}
	if s.Clock == nil {
		s.Clock = clock.Real{}
	}
	if s.LoginLimiter == nil {
		s.LoginLimiter = auth.NewRateLimiter(rate.Every(2*time.Second), 5, time.Second, 5*time.Minute)
	}
	if s.StepUpLimiter == nil {
		s.StepUpLimiter = auth.NewRateLimiter(rate.Every(2*time.Second), 5, time.Second, 5*time.Minute)
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// Handler returns the fully wrapped HTTP handler (security headers, CSRF,
// etc. — see middleware.go), suitable for http.Server.Handler.
func (s *Server) Handler() http.Handler {
	return s.withGlobalMiddleware(s.mux)
}

// NewHTTPServer builds an *http.Server bound to addr with CLAUDE.md's
// hardening timeouts.
func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

// clientIP resolves the request's real IP, trusting X-Forwarded-For only
// when the direct peer is TrustedProxy (CLAUDE.md: "only trust
// X-Forwarded-For from Newt").
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.TrustedProxy != "" && host == s.TrustedProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// The first entry is the original client.
			for i, c := range fwd {
				if c == ',' {
					return fwd[:i]
				}
			}
			return fwd
		}
	}
	return host
}

type ctxKey int

const ctxKeySession ctxKey = iota

func sessionFromContext(ctx context.Context) (store.Session, bool) {
	sess, ok := ctx.Value(ctxKeySession).(store.Session)
	return sess, ok
}

func withSession(ctx context.Context, sess store.Session) context.Context {
	return context.WithValue(ctx, ctxKeySession, sess)
}
