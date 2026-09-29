package web

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
	"github.com/Ultimatum22/powerwarden/internal/store"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

// All times handed to templates are already in the configured timezone
// (s.Loc): the store returns UTC, and templates only format.

// changeView is an upcoming schedule change.
type changeView struct {
	At    time.Time // local
	Label string    // "01:30", or "Mon 07:00" when not today
	On    bool      // true: turns on
	In    string    // e.g. "11h 20m"
}

// overrideView is an active override, for the amber strip.
type overrideView struct {
	ID         int64
	Action     string
	Until      *time.Time // local; nil = open-ended
	UntilLabel string
}

func (s *Server) overrideView(ov *store.Override) *overrideView {
	if ov == nil {
		return nil
	}
	v := &overrideView{ID: ov.ID, Action: ov.Action}
	if ov.Until != nil {
		t := ov.Until.In(s.Loc)
		v.Until = &t
		v.UntilLabel = s.whenLabel(t)
	}
	return v
}

// whenLabel is "15:04" for today, else "Mon 15:04" (within a week) or
// "2 Jan 15:04".
func (s *Server) whenLabel(t time.Time) string {
	now := s.Clock.Now().In(s.Loc)
	t = t.In(s.Loc)
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case t.Sub(now) < 6*24*time.Hour && t.After(now):
		return t.Format("Mon 15:04")
	default:
		return t.Format("2 Jan 15:04")
	}
}

// nextChange is sched's next boundary after now, within a week.
func (s *Server) nextChange(sched schedule.Schedule, now time.Time) *changeView {
	for _, b := range sched.Crossings(now, now.AddDate(0, 0, 8), s.Loc) {
		return &changeView{At: b.At.In(s.Loc), Label: s.whenLabel(b.At), On: b.On, In: humanDuration(b.At.Sub(now))}
	}
	return nil
}

// humanDuration renders d as "3d 4h", "11h 20m" or "5m".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// todaysWindows renders sched's windows that start on now's weekday, e.g.
// "17:00–00:30".
func (s *Server) todaysWindows(sched schedule.Schedule, now time.Time) string {
	day := now.In(s.Loc).Weekday()
	var parts []string
	for _, w := range sched {
		if w.Days.Has(day) {
			parts = append(parts, clockTime(w.On)+"–"+clockTime(w.Off))
		}
	}
	return strings.Join(parts, ", ")
}

func clockTime(t schedule.TimeOfDay) string {
	d := time.Duration(t)
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// guestView is a guest's display data, combining live Proxmox state,
// config, and any active override.
type guestView struct {
	Name         string
	Kind         string // "VM" | "LXC" | ""
	VMID         int
	Status       string
	Found        bool
	AlwaysOn     bool
	ScheduleName string
	Windows      string // today's windows, e.g. "17:00–00:30"
	Next         *changeView
	Override     *overrideView
}

func (s *Server) guestViews(ctx context.Context) ([]guestView, error) {
	guests, err := s.Proxmox.ListGuests(ctx)
	reachable := err == nil
	byName := make(map[string]proxmox.Guest, len(guests))
	for _, g := range guests {
		byName[g.Name] = g
	}

	now := s.Clock.Now()
	out := make([]guestView, 0, len(s.Guests))
	for _, gc := range s.Guests {
		gv := guestView{Name: gc.Name, AlwaysOn: gc.AlwaysOn, ScheduleName: gc.Schedule}
		if g, ok := byName[gc.Name]; ok {
			gv.Kind, gv.VMID, gv.Status, gv.Found = kindLabel(g.Kind), g.VMID, string(g.Status), true
		} else if !reachable {
			gv.Status = "unknown"
		} else if gc.AlwaysOn {
			// Expected: always-on guests are outside the API token's ACL
			// (CLAUDE.md), so the cluster listing doesn't include them.
			gv.Status = "protected"
		} else {
			gv.Status = "not found"
		}
		if !gc.AlwaysOn {
			sched := s.Schedules[gc.Schedule]
			gv.Windows = s.todaysWindows(sched, now)
			gv.Next = s.nextChange(sched, now)
			if ov, err := s.Store.EffectiveOverrideOf(ctx, gc.Name, now, store.PowerActions...); err == nil {
				gv.Override = s.overrideView(ov)
			}
		}
		out = append(out, gv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func kindLabel(k proxmox.GuestKind) string {
	switch k {
	case proxmox.KindQEMU:
		return "VM"
	case proxmox.KindLXC:
		return "LXC"
	}
	return ""
}

// hostView is the host's display data.
type hostView struct {
	Reachable    bool
	Uptime       int64
	ScheduleName string
	Windows      string
	Next         *changeView
	Override     *overrideView
}

func (s *Server) hostView(ctx context.Context) hostView {
	now := s.Clock.Now()
	sched := s.Schedules[s.Host.Schedule]
	hv := hostView{ScheduleName: s.Host.Schedule, Windows: s.todaysWindows(sched, now), Next: s.nextChange(sched, now)}
	if status, err := s.Proxmox.NodeStatus(ctx); err == nil {
		hv.Reachable = true
		hv.Uptime = status.Uptime
	}
	if ov, err := s.Store.EffectiveOverrideOf(ctx, "host", now, store.PowerActions...); err == nil {
		hv.Override = s.overrideView(ov)
	}
	return hv
}

// weatherView is the latest weather evaluation, as the engine last saw
// it: the web layer displays state, it doesn't make safety decisions.
type weatherView struct {
	Level       string // normal | watch | warning | danger
	Headline    string
	Nearest     string // nearest strike, or "—"
	Warning     string // active warning, or "None"
	Local       string // local sensor state
	Stale       bool
	Mode        string // notify | enforce
	ShutdownAt  *time.Time
	ShutdownIn  string
	EvaluatedAt *time.Time
	Ignored     string // ignore-weather override active until (label), or ""
}

func (s *Server) weatherView(ctx context.Context) weatherView {
	now := s.Clock.Now()
	wv := weatherView{Level: "normal", Nearest: "—", Warning: "None", Local: "—", Mode: "notify"}
	if ov, err := s.Store.EffectiveOverrideOf(ctx, "host", now, "ignore_weather"); err == nil && ov != nil && ov.Until != nil {
		wv.Ignored = s.whenLabel(*ov.Until)
	}
	if s.Engine == nil {
		if level, ok, err := s.Store.GetState(ctx, "weather_level"); err == nil && ok {
			wv.Level = level
		}
		wv.Headline = headline(weather.Result{}, wv.Level)
		return wv
	}

	st := s.Engine.LastWeather()
	r := st.Result
	wv.Mode = st.Mode
	if st.EvaluatedAt.IsZero() {
		wv.Headline = "Waiting for the first weather check"
		return wv
	}
	t := st.EvaluatedAt.In(s.Loc)
	wv.EvaluatedAt = &t
	wv.Level = r.Level.String()
	wv.Stale = r.Stale
	if r.NearestStrikeKM >= 0 {
		wv.Nearest = fmt.Sprintf("%.0f km", r.NearestStrikeKM)
	}
	if r.ActiveWarning != nil {
		wv.Warning = strings.TrimSpace(r.ActiveWarning.Color + " " + r.ActiveWarning.Event)
	}
	if r.LocalConfirmed {
		wv.Local = "Lightning"
	} else {
		wv.Local = "Quiet"
	}
	if st.ShutdownAt != nil {
		at := st.ShutdownAt.In(s.Loc)
		wv.ShutdownAt = &at
		wv.ShutdownIn = humanDuration(max(at.Sub(now), 0))
	}
	wv.Headline = headline(r, wv.Level)
	if w := s.thunderWindow(r.ThunderHours, now); w != "" && (wv.Level == "watch" || wv.Level == "normal") {
		wv.Headline = "Thunderstorms possible " + w
	}
	return wv
}

// thunderWindow renders the first run of consecutive forecast thunder
// hours that hasn't ended yet, e.g. "18:00–22:00" or "Wed 18:00–22:00".
func (s *Server) thunderWindow(hours []time.Time, now time.Time) string {
	var start, end time.Time
	for _, h := range hours {
		if !h.Add(time.Hour).After(now) {
			continue // already over
		}
		switch {
		case start.IsZero():
			start, end = h, h.Add(time.Hour)
		case h.Equal(end):
			end = h.Add(time.Hour)
		default:
			return s.whenLabel(start) + "–" + end.In(s.Loc).Format("15:04")
		}
	}
	if start.IsZero() {
		return ""
	}
	return s.whenLabel(start) + "–" + end.In(s.Loc).Format("15:04")
}

func headline(r weather.Result, level string) string {
	switch level {
	case "danger":
		if r.LocalConfirmed {
			return "Lightning detected here"
		}
		if r.NearestStrikeKM >= 0 {
			return fmt.Sprintf("Lightning %.0f km away", r.NearestStrikeKM)
		}
		return "Thunderstorm danger"
	case "warning":
		if r.ActiveWarning != nil {
			return "Official " + strings.TrimSpace(r.ActiveWarning.Color+" thunderstorm warning")
		}
		if r.NearestStrikeKM >= 0 {
			return fmt.Sprintf("Lightning %.0f km away", r.NearestStrikeKM)
		}
		return "Thunderstorm warning"
	case "watch":
		return "Thunderstorms possible in the coming hours"
	default:
		return "No thunderstorms expected"
	}
}

// eventView is one audit-log row, described for people.
type eventView struct {
	When   string
	Text   string
	Actor  string
	IP     string
	DryRun bool
}

func (s *Server) eventViews(events []store.Event) []eventView {
	out := make([]eventView, 0, len(events))
	for _, e := range events {
		out = append(out, eventView{
			When:   e.At.In(s.Loc).Format("2 Jan 15:04:05"),
			Text:   describeEvent(e),
			Actor:  e.Actor,
			IP:     e.IP,
			DryRun: e.DryRun,
		})
	}
	return out
}

func describeEvent(e store.Event) string {
	target := e.Target
	if target == "host" {
		target = "Host"
	}
	with := func(s string) string {
		if e.Reason != "" {
			return s + " (" + e.Reason + ")"
		}
		return s
	}
	switch e.Kind {
	case "guest_start":
		return target + " started"
	case "guest_shutdown":
		return target + " shut down"
	case "host_shutdown":
		return "Host shut down"
	case "host_wake":
		return "Wake-on-LAN sent to the host"
	case "host_wake_ok":
		return "Host is up after Wake-on-LAN"
	case "host_wake_failed":
		return "Wake-on-LAN failed"
	case "login_ok":
		return "Signed in"
	case "login_fail":
		return "Failed sign-in"
	case "weather_level":
		return with("Weather level changed")
	case "weather_ignore":
		return with("Weather safety bypassed")
	case "override_created":
		return with("Override on " + target)
	case "override_cancelled":
		return "Override cancelled"
	case "vacation_start":
		return with("Vacation started")
	case "vacation_end":
		return "Vacation ended"
	case "schedules_paused":
		return "Schedules paused: " + target
	case "passkey_added":
		return "Passkey added"
	case "passkey_removed":
		return "Passkey removed"
	case "totp_enabled":
		return "Authenticator app set up"
	case "totp_disabled":
		return "Authenticator app removed"
	case "session_revoked":
		return "Session revoked"
	}
	return strings.TrimSpace(with(e.Kind + " " + e.Target))
}
