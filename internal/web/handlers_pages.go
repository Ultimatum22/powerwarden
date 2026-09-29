package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

func (s *Server) pd(r *http.Request, active, title string) pageData {
	sess, _ := sessionFromContext(r.Context())
	pd := pageData{Title: title, ActiveNav: active, CSRFToken: s.csrfToken(sess), Now: s.Clock.Now().In(s.Loc).Format("Mon 2 Jan 15:04")}
	pd.Vacation = s.activeVacation(r.Context()) != nil
	if !sess.CreatedAt.IsZero() {
		remaining := s.Sessions.AbsoluteTimeout - s.Clock.Now().Sub(sess.CreatedAt)
		if remaining > 0 {
			pd.SessionEnd = humanDuration(remaining)
		}
	}
	return pd
}

type dashboardData struct {
	Host     hostView
	Weather  weatherView
	Guests   []guestView
	Upcoming []timelineEntry
	Recent   []eventView
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestViews(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := dashboardData{Host: s.hostView(r.Context()), Weather: s.weatherView(r.Context()), Guests: guests}
	if data.Upcoming, err = s.upcoming(r.Context(), s.Clock.Now(), 24*time.Hour, 5); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	recent, err := s.Store.ListEvents(r.Context(), store.EventQuery{Limit: 5})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Recent = s.eventViews(recent)
	pd := s.pd(r, "dashboard", "Dashboard")
	pd.Data = data
	s.render(w, "dashboard", pd)
}

type hostShutdownData struct {
	Host          hostView
	Guests        []guestView
	ActiveTasks   int
	TasksErr      error
	AlwaysOnNames []string
	NextOn        *time.Time // when "wake at next schedule" wakes the host
	Scrubbing     []string   // ZFS pools with a scrub running
	ScrubErr      error
	LastWakeOK    *time.Time // last Wake-on-LAN that brought the host up
}

// ChecksPass reports whether every pre-check passed; the confirm button
// is disabled otherwise (CLAUDE.md: "if a check fails, show it and
// disable confirm"). Active tasks don't block: the engine postpones the
// shutdown until they finish, as the page says.
func (d hostShutdownData) ChecksPass() bool {
	return d.TasksErr == nil && d.ScrubErr == nil && len(d.Scrubbing) == 0
}

func (s *Server) handleHostShutdownPage(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestViews(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := hostShutdownData{Host: s.hostView(r.Context()), Guests: guests}
	if t := s.nextHostOn(s.Clock.Now()); t != nil {
		local := t.In(s.Loc)
		data.NextOn = &local
	}
	if tasks, err := s.Proxmox.ActiveTasks(r.Context()); err != nil {
		data.TasksErr = err
	} else {
		data.ActiveTasks = len(tasks)
	}
	data.Scrubbing, data.ScrubErr = s.Proxmox.ZFSScrubsInProgress(r.Context())
	if v, ok, _ := s.Store.GetState(r.Context(), engine.HostLastWakeOKKey); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			local := t.In(s.Loc)
			data.LastWakeOK = &local
		}
	}
	for _, g := range guests {
		if g.AlwaysOn {
			data.AlwaysOnNames = append(data.AlwaysOnNames, g.Name)
		}
	}

	pd := s.pd(r, "dashboard", "Shut down host")
	pd.Data = data
	s.render(w, "host_shutdown", pd)
}

func (s *Server) handleVacationPage(w http.ResponseWriter, r *http.Request) {
	pd := s.pd(r, "vacation", "Vacation")
	pd.Data = s.hostView(r.Context())
	s.render(w, "vacation", pd)
}

const eventsPageSize = 50

type eventsData struct {
	Events   []eventView
	Kinds    []string
	Kind     string
	Newer    bool   // not on the first page
	OlderURL string // next page, or ""
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := store.EventQuery{Kind: r.URL.Query().Get("kind"), Limit: eventsPageSize}
	data := eventsData{Kind: q.Kind}
	if v := r.URL.Query().Get("before"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		before, err := s.Store.GetEvent(r.Context(), id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		q.Before, data.Newer = &before, true
	}
	events, err := s.Store.ListEvents(r.Context(), q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data.Kinds, err = s.Store.EventKinds(r.Context()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Events = s.eventViews(events)
	if len(events) == eventsPageSize {
		v := url.Values{"before": {strconv.FormatInt(events[len(events)-1].ID, 10)}}
		if q.Kind != "" {
			v.Set("kind", q.Kind)
		}
		data.OlderURL = "/events?" + v.Encode()
	}
	pd := s.pd(r, "events", "Events")
	pd.Data = data
	s.render(w, "events", pd)
}

func (s *Server) handleWeatherPage(w http.ResponseWriter, r *http.Request) {
	pd := s.pd(r, "weather", "Weather")
	pd.Data = struct{ Weather weatherView }{s.weatherView(r.Context())}
	s.render(w, "weather", pd)
}

type securityData struct {
	Credentials []credentialView
	Sessions    []sessionView
	HasTOTP     bool
	Attempts    []store.Event
}

type credentialView struct {
	IDHex     string
	CreatedAt time.Time
	LastUsed  *time.Time
}

type sessionView struct {
	IDHex     string
	CreatedAt time.Time
	LastSeen  time.Time
	IP        string
	UserAgent string
	Current   bool
}

func (s *Server) handleSecurityPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFromContext(r.Context())
	u, err := s.Store.GetUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	creds, err := s.Store.CredentialsByUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sessions, err := s.Store.SessionsByUser(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := securityData{HasTOTP: len(u.TOTPSecretEnc) > 0}
	for _, c := range creds {
		cv := credentialView{IDHex: hexID(c.ID), CreatedAt: c.CreatedAt.In(s.Loc)}
		if c.LastUsed != nil {
			t := c.LastUsed.In(s.Loc)
			cv.LastUsed = &t
		}
		data.Credentials = append(data.Credentials, cv)
	}
	for _, sv := range sessions {
		data.Sessions = append(data.Sessions, sessionView{
			IDHex: hexID(sv.TokenHash), CreatedAt: sv.CreatedAt.In(s.Loc), LastSeen: sv.LastSeen.In(s.Loc),
			IP: sv.IP, UserAgent: sv.UserAgent, Current: string(sv.TokenHash) == string(sess.TokenHash),
		})
	}

	if data.Attempts, err = s.recentLoginAttempts(r, 10); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for i := range data.Attempts {
		data.Attempts[i].At = data.Attempts[i].At.In(s.Loc)
	}

	pd := s.pd(r, "security", "Security")
	pd.Data = data
	s.render(w, "security", pd)
}

func hexID(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hextable[v>>4]
		out[i*2+1] = hextable[v&0x0f]
	}
	return string(out)
}
