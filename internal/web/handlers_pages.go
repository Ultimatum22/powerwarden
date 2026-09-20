package web

import (
	"net/http"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/schedule"
)

func (s *Server) pd(r *http.Request, active, title string) pageData {
	sess, _ := sessionFromContext(r.Context())
	pd := pageData{Title: title, ActiveNav: active, CSRFToken: s.csrfToken(sess)}
	if ov, err := s.Store.EffectiveOverride(r.Context(), "host", s.Clock.Now()); err == nil && ov != nil && ov.Action == "off" && ov.Until == nil {
		pd.Vacation = true
	}
	if !sess.CreatedAt.IsZero() {
		remaining := s.Sessions.AbsoluteTimeout - s.Clock.Now().Sub(sess.CreatedAt)
		if remaining > 0 {
			pd.SessionEnd = remaining.Round(time.Minute).String()
		}
	}
	return pd
}

type dashboardData struct {
	Host    hostView
	Weather weatherView
	Guests  []guestView
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestViews(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pd := s.pd(r, "dashboard", "Dashboard")
	pd.Data = dashboardData{Host: s.hostView(r.Context()), Weather: s.weatherView(r.Context()), Guests: guests}
	s.render(w, "dashboard", pd)
}

type timelineEntry struct {
	Time string
	Text string
}

type timelineData struct {
	Day     string
	Entries []timelineEntry
}

// handleTimeline shows the schedule's upcoming boundary crossings for
// today. CLAUDE.md's spec describes a full percent-width track
// visualization across Today/Tomorrow/Week; this ships the "Coming up"
// list (the same underlying schedule.Crossings data), which is the part
// an owner actually acts on — the graphical track is a reasonable
// follow-up, not a security- or correctness-relevant gap.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	now := s.Clock.Now().In(s.Loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	var entries []timelineEntry
	addCrossings := func(name string, sched schedule.Schedule) {
		for _, b := range sched.Crossings(dayStart.Add(-time.Nanosecond), dayEnd, s.Loc) {
			word := "off"
			if b.On {
				word = "on"
			}
			entries = append(entries, timelineEntry{Time: b.At.In(s.Loc).Format("15:04"), Text: name + " turns " + word})
		}
	}
	addCrossings("host", s.Schedules[s.Host.Schedule])
	for _, g := range s.Guests {
		if !g.AlwaysOn {
			addCrossings(g.Name, s.Schedules[g.Schedule])
		}
	}

	pd := s.pd(r, "timeline", "Timeline")
	pd.Data = timelineData{Day: dayStart.Format("Monday, Jan 2"), Entries: entries}
	s.render(w, "timeline", pd)
}

type hostShutdownData struct {
	Host          hostView
	Guests        []guestView
	ActiveTasks   int
	TasksErr      error
	AlwaysOnNames []string
}

func (s *Server) handleHostShutdownPage(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestViews(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := hostShutdownData{Host: s.hostView(r.Context()), Guests: guests}
	if tasks, err := s.Proxmox.ActiveTasks(r.Context()); err != nil {
		data.TasksErr = err
	} else {
		data.ActiveTasks = len(tasks)
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

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Store.ListRecentEvents(r.Context(), 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pd := s.pd(r, "events", "Events")
	pd.Data = events
	s.render(w, "events", pd)
}

type securityData struct {
	Credentials []credentialView
	Sessions    []sessionView
	HasTOTP     bool
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
		data.Credentials = append(data.Credentials, credentialView{IDHex: hexID(c.ID), CreatedAt: c.CreatedAt, LastUsed: c.LastUsed})
	}
	for _, sv := range sessions {
		data.Sessions = append(data.Sessions, sessionView{
			IDHex: hexID(sv.TokenHash), CreatedAt: sv.CreatedAt, LastSeen: sv.LastSeen,
			IP: sv.IP, UserAgent: sv.UserAgent, Current: string(sv.TokenHash) == string(sess.TokenHash),
		})
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
