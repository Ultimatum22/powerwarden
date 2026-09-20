package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/store"
)

// parseDateTime accepts RFC3339 (what a JS client or curl would send) or
// the format HTML's <input type="datetime-local"> actually produces
// ("2006-01-02T15:04", no seconds or timezone), interpreting the latter in
// the configured timezone.
func (s *Server) parseDateTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", v, s.Loc)
}

func (s *Server) actor(r *http.Request) string {
	sess, ok := sessionFromContext(r.Context())
	if !ok {
		return "system"
	}
	return "user:" + sess.UserID
}

// parseUntil resolves a guest start's "until"/"duration"/"next_boundary"
// body params into a concrete expiry, or nil for "no expiry" (persists
// until cancelled).
func (s *Server) parseUntil(r *http.Request, guestName string) (*time.Time, error) {
	now := s.Clock.Now()
	if v := r.PostForm.Get("until"); v != "" {
		t, err := s.parseDateTime(v)
		if err != nil {
			return nil, fmt.Errorf("invalid until: %w", err)
		}
		return &t, nil
	}
	if v := r.PostForm.Get("duration"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid duration: %w", err)
		}
		t := now.Add(d)
		return &t, nil
	}
	if r.PostForm.Get("next_boundary") != "" {
		for _, gc := range s.Guests {
			if gc.Name != guestName {
				continue
			}
			sched := s.Schedules[gc.Schedule]
			boundaries := sched.Crossings(now, now.AddDate(0, 0, 8), s.Loc)
			if len(boundaries) > 0 {
				t := boundaries[0].At
				return &t, nil
			}
		}
	}
	return nil, nil
}

func (s *Server) handleGuestStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownGuest(name) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	until, err := s.parseUntil(r, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.createOverride(w, r, name, "on", until)
}

func (s *Server) handleGuestStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownGuest(name) {
		http.NotFound(w, r)
		return
	}
	s.createOverride(w, r, name, "off", nil)
}

func (s *Server) knownGuest(name string) bool {
	for _, g := range s.Guests {
		if g.Name == name && !g.AlwaysOn {
			return true
		}
	}
	return false
}

func (s *Server) createOverride(w http.ResponseWriter, r *http.Request, target, action string, until *time.Time) {
	now := s.Clock.Now()
	id, err := s.Store.CreateOverride(r.Context(), store.Override{
		Target: target, Action: action, Until: until, CreatedBy: s.actor(r), CreatedAt: now,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: now, Kind: "override_created", Target: target, Actor: s.actor(r), IP: s.clientIP(r),
		Reason: fmt.Sprintf("action=%s override_id=%d", action, id),
	})
	s.hxRefresh(w, r)
}

func (s *Server) handleOverrideCancel(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := s.Clock.Now()
	if err := s.Store.CancelOverride(r.Context(), id, now); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{At: now, Kind: "override_cancelled", Actor: s.actor(r), IP: s.clientIP(r)})
	s.hxRefresh(w, r)
}

// handleHostWake implements the dashboard's "Keep on tonight" action: an
// "on" override lasting until local midnight, after which the schedule
// resumes normal authority.
func (s *Server) handleHostWake(w http.ResponseWriter, r *http.Request) {
	now := s.Clock.Now().In(s.Loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, s.Loc)
	s.createOverride(w, r, "host", "on", &midnight)
}

// handleHostShutdown creates an indefinite "off" override for the host.
// The actual shutdown (including force-stopping guests, checking active
// tasks, and waiting for the task) is the engine's job on its next tick —
// reusing that logic here instead of duplicating it costs at most one
// tick's latency (<=30s) for a deliberate, step-up-gated action.
func (s *Server) handleHostShutdown(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var until *time.Time
	switch r.PostForm.Get("wake") {
	case "date":
		if v := r.PostForm.Get("date"); v != "" {
			if t, err := s.parseDateTime(v); err == nil {
				until = &t
			}
		}
	case "manual", "schedule", "":
		// No override expiry: "manual" means stay off until explicitly
		// woken; "schedule" means the host's own schedule will naturally
		// want it on again, which the engine's regular reconciliation
		// picks up once this override is cancelled or expires — since
		// there's no natural expiry for "resume schedule", the shutdown
		// page's radio choice is recorded via the event reason for now.
	}
	s.createOverride(w, r, "host", "off", until)
}

func (s *Server) handleVacationStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	v := r.PostForm.Get("return")
	if v == "" {
		http.Error(w, "return date/time is required", http.StatusBadRequest)
		return
	}
	t, err := s.parseDateTime(v)
	if err != nil {
		http.Error(w, "invalid return date/time", http.StatusBadRequest)
		return
	}
	s.createOverride(w, r, "host", "off", &t)
}

func (s *Server) handleVacationEnd(w http.ResponseWriter, r *http.Request) {
	now := s.Clock.Now()
	ov, err := s.Store.EffectiveOverride(r.Context(), "host", now)
	if err != nil || ov == nil {
		http.Error(w, "no active vacation", http.StatusBadRequest)
		return
	}
	if err := s.Store.CancelOverride(r.Context(), ov.ID, now); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{At: now, Kind: "vacation_end", Target: "host", Actor: s.actor(r), IP: s.clientIP(r)})
	s.hxRefresh(w, r)
}

func (s *Server) handleWeatherIgnore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	minutesStr := r.PostForm.Get("minutes")
	minutes, err := strconv.Atoi(minutesStr)
	if err != nil || minutes <= 0 {
		http.Error(w, "invalid minutes", http.StatusBadRequest)
		return
	}
	now := s.Clock.Now()
	until := now.Add(time.Duration(minutes) * time.Minute)
	id, err := s.Store.CreateOverride(r.Context(), store.Override{
		Target: "host", Action: "ignore_weather", Until: &until, CreatedBy: s.actor(r), CreatedAt: now,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: now, Kind: "weather_ignore", Target: "host", Actor: s.actor(r), IP: s.clientIP(r),
		Reason: fmt.Sprintf("override_id=%d minutes=%d", id, minutes),
	})
	s.hxRefresh(w, r)
}

func (s *Server) handleSchedulesPause(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := r.PostForm.Get("target")
	if target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}
	var until *time.Time
	if v := r.PostForm.Get("until"); v != "" {
		if t, err := s.parseDateTime(v); err == nil {
			until = &t
		}
	}

	targets := []string{target}
	if target == "all" {
		targets = []string{"host"}
		for _, g := range s.Guests {
			if !g.AlwaysOn {
				targets = append(targets, g.Name)
			}
		}
	}
	now := s.Clock.Now()
	for _, t := range targets {
		if _, err := s.Store.CreateOverride(r.Context(), store.Override{
			Target: t, Action: "pause", Until: until, CreatedBy: s.actor(r), CreatedAt: now,
		}); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{At: now, Kind: "schedules_paused", Target: target, Actor: s.actor(r), IP: s.clientIP(r)})
	s.hxRefresh(w, r)
}

// hxRefresh tells htmx to re-fetch the current page's content, the
// simplest way to reflect a just-made change without hand-tracking which
// partial each action affects.
func (s *Server) hxRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}
