package web

import (
	"context"
	"sort"

	"github.com/Ultimatum22/powerwarden/internal/proxmox"
	"github.com/Ultimatum22/powerwarden/internal/store"
)

// guestView is a guest's display data, combining live Proxmox state,
// config, and any active override.
type guestView struct {
	Name         string
	Kind         string
	VMID         int
	Status       string
	Found        bool
	AlwaysOn     bool
	ScheduleName string
	Override     *store.Override
}

func (s *Server) guestViews(ctx context.Context) ([]guestView, error) {
	guests, err := s.Proxmox.ListGuests(ctx)
	reachable := err == nil
	byName := make(map[string]proxmox.Guest, len(guests))
	for _, g := range guests {
		byName[g.Name] = g
	}

	when := s.Clock.Now()
	out := make([]guestView, 0, len(s.Guests))
	for _, gc := range s.Guests {
		gv := guestView{Name: gc.Name, AlwaysOn: gc.AlwaysOn, ScheduleName: gc.Schedule}
		if g, ok := byName[gc.Name]; ok {
			gv.Kind, gv.VMID, gv.Status, gv.Found = string(g.Kind), g.VMID, string(g.Status), true
		} else if !reachable {
			gv.Status = "unknown (host unreachable)"
		} else {
			gv.Status = "not found"
		}
		if !gc.AlwaysOn {
			ov, err := s.Store.EffectiveOverride(ctx, gc.Name, when)
			if err == nil {
				gv.Override = ov
			}
		}
		out = append(out, gv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// hostView is the host's display data.
type hostView struct {
	Reachable    bool
	Uptime       int64
	ScheduleName string
	Override     *store.Override
}

func (s *Server) hostView(ctx context.Context) hostView {
	hv := hostView{ScheduleName: s.Host.Schedule}
	if status, err := s.Proxmox.NodeStatus(ctx); err == nil {
		hv.Reachable = true
		hv.Uptime = status.Uptime
	}
	if ov, err := s.Store.EffectiveOverride(ctx, "host", s.Clock.Now()); err == nil {
		hv.Override = ov
	}
	return hv
}

// weatherView reads the engine's last-evaluated level from the state
// table (set by internal/engine's reconcileWeather), rather than
// re-evaluating the Monitor here — the web layer displays state, it
// doesn't make safety decisions.
type weatherView struct {
	Level string
}

func (s *Server) weatherView(ctx context.Context) weatherView {
	level, ok, err := s.Store.GetState(ctx, "weather_level")
	if err != nil || !ok {
		return weatherView{Level: "normal"}
	}
	return weatherView{Level: level}
}
