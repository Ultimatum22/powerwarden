package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/engine"
	"github.com/Ultimatum22/powerwarden/internal/schedule"
)

// Timeline tracks are inline SVG: a <rect>'s x/width are presentation
// attributes, not styles, so they work under the CSP's style-src 'self'
// (an inline style="width:…" would be blocked). Coordinates run 0–1000.
const trackUnits = 1000.0

type segment struct {
	X, W  float64
	Class string // seg-schedule | seg-override | seg-alwayson
}

type trackRow struct {
	Label    string
	Summary  string // text alternative for the track
	Segments []segment
}

type timelineEntry struct {
	At    time.Time
	Label string
	Text  string
}

type timelineData struct {
	View     string // today | tomorrow | week
	Title    string
	Axis     []string
	NowX     float64 // -1 when now is outside the range
	Rows     []trackRow
	Upcoming []timelineEntry
}

// trackState is what a row shows at one instant.
type trackState string

const (
	stateOff      trackState = ""
	stateSchedule trackState = "seg-schedule"
	stateOverride trackState = "seg-override"
	stateAlwaysOn trackState = "seg-alwayson"
)

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	now := s.Clock.Now().In(s.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)

	data := timelineData{View: r.URL.Query().Get("day")}
	var from, to time.Time
	step := 5 * time.Minute
	switch data.View {
	case "tomorrow":
		from, to = today.AddDate(0, 0, 1), today.AddDate(0, 0, 2)
		data.Title = from.Format("Monday 2 January")
		data.Axis = []string{"00", "06", "12", "18", "24"}
	case "week":
		from, to = today, today.AddDate(0, 0, 7)
		step = 15 * time.Minute
		data.Title = "Next 7 days"
		for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
			data.Axis = append(data.Axis, d.Format("Mon"))
		}
	default:
		data.View = "today"
		from, to = today, today.AddDate(0, 0, 1)
		data.Title = from.Format("Monday 2 January")
		data.Axis = []string{"00", "06", "12", "18", "24"}
	}

	data.NowX = -1
	if !now.Before(from) && now.Before(to) {
		data.NowX = trackUnits * float64(now.Sub(from)) / float64(to.Sub(from))
	}

	rows, err := s.timelineRows(r.Context(), from, to, step)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Rows = rows
	if data.Upcoming, err = s.upcoming(r.Context(), now, 24*time.Hour, 20); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pd := s.pd(r, "timeline", "Timeline")
	pd.Data = data
	s.render(w, "timeline", pd)
}

func (s *Server) timelineRows(ctx context.Context, from, to time.Time, step time.Duration) ([]trackRow, error) {
	var rows []trackRow
	host, err := s.track(ctx, "Host", "host", s.Schedules[s.Host.Schedule], false, from, to, step)
	if err != nil {
		return nil, err
	}
	rows = append(rows, host)
	guests := append([]string(nil), guestNames(s)...)
	sort.Strings(guests)
	for _, name := range guests {
		gc := s.guestConfig(name)
		row, err := s.track(ctx, name, name, s.Schedules[gc.Schedule], gc.AlwaysOn, from, to, step)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if s.Engine != nil {
		rows = append(rows, s.stormTrack(s.Engine.LastWeather().Result.ThunderHours, from, to))
	}
	return rows, nil
}

// stormTrack marks forecast hours with a thunderstorm code.
func (s *Server) stormTrack(hours []time.Time, from, to time.Time) trackRow {
	row := trackRow{Label: "Storm risk", Summary: "Storm risk (forecast): none in this period"}
	span := float64(to.Sub(from))
	n := 0
	for _, h := range hours {
		start, end := h, h.Add(time.Hour)
		if !end.After(from) || !start.Before(to) {
			continue
		}
		start, end = maxTime(start, from), minTime(end, to)
		row.Segments = append(row.Segments, segment{
			X: trackUnits * float64(start.Sub(from)) / span,
			W: trackUnits * float64(end.Sub(start)) / span,
			Class: "seg-storm",
		})
		n++
	}
	if n > 0 {
		row.Summary = fmt.Sprintf("Storm risk (forecast): thunderstorms possible in %d hour(s) of this period", n)
	}
	return row
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// track samples one row's state every step and merges equal neighbours
// into segments. Overrides are loaded once for the whole range.
func (s *Server) track(ctx context.Context, label, target string, sched schedule.Schedule, alwaysOn bool, from, to time.Time, step time.Duration) (trackRow, error) {
	row := trackRow{Label: label}
	if alwaysOn {
		row.Segments = []segment{{X: 0, W: trackUnits, Class: string(stateAlwaysOn)}}
		row.Summary = label + ": always on"
		return row, nil
	}
	overrides, err := s.Store.OverridesOverlapping(ctx, target, from, to)
	if err != nil {
		return row, err
	}
	stateAt := func(t time.Time) trackState {
		for _, ov := range overrides {
			if !ov.ActiveAt(t) {
				continue
			}
			switch ov.Action {
			case "on", "pause":
				return stateOverride
			case "off":
				return stateOff
			}
		}
		if sched.IsOn(t, s.Loc) {
			return stateSchedule
		}
		return stateOff
	}

	span := float64(to.Sub(from))
	var cur trackState
	var start time.Time
	var onFor time.Duration
	flush := func(end time.Time) {
		if cur != stateOff {
			row.Segments = append(row.Segments, segment{
				X:     trackUnits * float64(start.Sub(from)) / span,
				W:     trackUnits * float64(end.Sub(start)) / span,
				Class: string(cur),
			})
			onFor += end.Sub(start)
		}
	}
	for t := from; t.Before(to); t = t.Add(step) {
		st := stateAt(t)
		if t.Equal(from) {
			cur, start = st, t
			continue
		}
		if st != cur {
			flush(t)
			cur, start = st, t
		}
	}
	flush(to)
	row.Summary = fmt.Sprintf("%s: on for %s of this period", label, humanDuration(onFor))
	return row, nil
}

func guestNames(s *Server) []string {
	out := make([]string, 0, len(s.Guests))
	for _, g := range s.Guests {
		out = append(out, g.Name)
	}
	return out
}

// upcoming lists schedule changes and override expiries within horizon,
// soonest first, e.g. "01:30 Host shuts down · wakes Mon 07:00 via
// Wake-on-LAN".
func (s *Server) upcoming(ctx context.Context, now time.Time, horizon time.Duration, limit int) ([]timelineEntry, error) {
	end := now.Add(horizon)
	var out []timelineEntry
	add := func(at time.Time, text string) {
		out = append(out, timelineEntry{At: at, Label: s.whenLabel(at), Text: text})
	}

	hostSched := s.Schedules[s.Host.Schedule]
	for _, b := range hostSched.Crossings(now, end, s.Loc) {
		if b.On {
			add(b.At, "Host wakes via Wake-on-LAN")
			continue
		}
		text := "Host shuts down"
		if next := s.nextChange(hostSched, b.At); next != nil && next.On {
			text += " · wakes " + s.whenLabel(next.At) + " via Wake-on-LAN"
		}
		add(b.At, text)
	}
	for _, g := range s.Guests {
		if g.AlwaysOn {
			continue
		}
		for _, b := range s.Schedules[g.Schedule].Crossings(now, end, s.Loc) {
			if b.On {
				add(b.At, g.Name+" starts (schedule "+g.Schedule+")")
			} else {
				add(b.At, g.Name+" shuts down (schedule "+g.Schedule+")")
			}
		}
	}
	for _, target := range append([]string{"host"}, guestNames(s)...) {
		ovs, err := s.Store.OverridesOverlapping(ctx, target, now, end)
		if err != nil {
			return nil, err
		}
		for _, ov := range ovs {
			if ov.Until == nil || !ov.Until.After(now) || ov.Until.After(end) || !ov.ActiveAt(now) {
				continue
			}
			name := target
			if target == "host" {
				name = "Host"
			}
			what := map[string]string{"on": "override", "off": "override", "pause": "pause", "ignore_weather": "weather bypass"}[ov.Action]
			add(*ov.Until, fmt.Sprintf("%s %s ends, schedule takes over", name, what))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Server) guestConfig(name string) engine.GuestConfig {
	for _, g := range s.Guests {
		if g.Name == name {
			return g
		}
	}
	return engine.GuestConfig{Name: name}
}
