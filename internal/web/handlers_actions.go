package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/notify"
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
	if _, err := s.insertOverride(r, target, action, until); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.hxRefresh(w, r)
}

func (s *Server) insertOverride(r *http.Request, target, action string, until *time.Time) (int64, error) {
	now := s.Clock.Now()
	id, err := s.Store.CreateOverride(r.Context(), store.Override{
		Target: target, Action: action, Until: until, CreatedBy: s.actor(r), CreatedAt: now,
	})
	if err != nil {
		return 0, err
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: now, Kind: "override_created", Target: target, Actor: s.actor(r), IP: s.clientIP(r),
		Reason: fmt.Sprintf("action=%s override_id=%d", action, id),
	})
	return id, nil
}

// nextHostOn is when the host schedule next turns the host on, within
// the coming week, or nil if it never does.
func (s *Server) nextHostOn(now time.Time) *time.Time {
	for _, b := range s.Schedules[s.Host.Schedule].Crossings(now, now.AddDate(0, 0, 8), s.Loc) {
		if b.On {
			t := b.At
			return &t
		}
	}
	return nil
}

// futureTime parses a form date/time and requires it to lie between now
// and a year from now.
func (s *Server) futureTime(v string) (time.Time, bool) {
	t, err := s.parseDateTime(v)
	now := s.Clock.Now()
	if err != nil || !t.After(now) || t.After(now.AddDate(1, 0, 0)) {
		return time.Time{}, false
	}
	return t, true
}

// vacationStateKey holds the ID of the override that implements the
// current vacation, telling it apart from a plain manual host shutdown
// (both are host "off" overrides).
const vacationStateKey = "web.vacation_override_id"

// activeVacation returns the vacation override if one is in effect.
func (s *Server) activeVacation(ctx context.Context) *store.Override {
	idStr, ok, err := s.Store.GetState(ctx, vacationStateKey)
	if err != nil || !ok {
		return nil
	}
	ov, err := s.Store.EffectiveOverrideOf(ctx, "host", s.Clock.Now(), store.PowerActions...)
	if err != nil || ov == nil || strconv.FormatInt(ov.ID, 10) != idStr {
		return nil
	}
	return ov
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
	case "schedule", "":
		// Off until the host schedule next turns on; the override then
		// expires and the schedule (with WoL) takes over. A schedule that
		// never turns on within a week behaves like "manual".
		until = s.nextHostOn(s.Clock.Now())
	case "date":
		t, ok := s.futureTime(r.PostForm.Get("date"))
		if !ok {
			http.Error(w, "choose a wake-up date in the future", http.StatusBadRequest)
			return
		}
		until = &t
	case "manual":
		// No expiry: stays off until woken from the dashboard or CLI.
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Back to the dashboard rather than reloading the confirmation page.
	w.Header().Set("HX-Redirect", "/")
	s.createOverride(w, r, "host", "off", until)
}

func (s *Server) handleVacationStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	t, ok := s.futureTime(r.PostForm.Get("return"))
	if !ok {
		http.Error(w, "choose a return date in the future", http.StatusBadRequest)
		return
	}
	id, err := s.insertOverride(r, "host", "off", &t)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.Store.SetState(r.Context(), vacationStateKey, strconv.FormatInt(id, 10)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.RecordEvent(r.Context(), store.Event{
		At: s.Clock.Now(), Kind: "vacation_start", Target: "host", Actor: s.actor(r), IP: s.clientIP(r),
		Reason: "until " + t.In(s.Loc).Format(time.RFC3339),
	})
	s.hxRefresh(w, r)
}

func (s *Server) handleVacationEnd(w http.ResponseWriter, r *http.Request) {
	now := s.Clock.Now()
	ov := s.activeVacation(r.Context())
	if ov == nil {
		http.Error(w, "no active vacation", http.StatusBadRequest)
		return
	}
	if err := s.Store.CancelOverride(r.Context(), ov.ID, now); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.Store.DeleteState(r.Context(), vacationStateKey)
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
	if err != nil || minutes <= 0 || minutes > maxIgnoreWeatherMinutes {
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
	// CLAUDE.md: notify on any ignore-weather action.
	s.notify(r, notify.Notification{
		Title:    "labpower: weather safety bypassed",
		Body:     fmt.Sprintf("%s ignored weather safety for %d minutes (until %s) from %s.", s.actor(r), minutes, until.In(s.Loc).Format("15:04"), s.clientIP(r)),
		Priority: notify.PriorityHigh,
	})
	s.hxRefresh(w, r)
}

// maxIgnoreWeatherMinutes bounds one ignore-weather action, so a single
// step-up can't switch storm protection off indefinitely.
const maxIgnoreWeatherMinutes = 24 * 60

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
