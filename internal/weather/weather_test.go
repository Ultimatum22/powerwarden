package weather

import (
	"context"
	"errors"
	"testing"
	"time"
)

var home = Point{Lat: 52.37, Lon: 4.90} // Amsterdam, per CLAUDE.md's example config

func nearby(km float64) Point {
	// Roughly km north of home; good enough for these tests (haversine
	// itself is tested separately).
	return Point{Lat: home.Lat + km/110.0, Lon: home.Lon}
}

func baseConfig() Config {
	return Config{
		Location:        home,
		WarningRadiusKM: 30,
		DangerRadiusKM:  12,
		AllClearAfter:   30 * time.Minute,
		StaleAfter:      10 * time.Minute,
	}
}

func TestNormalWithNoSignals(t *testing.T) {
	cfg := baseConfig()
	cfg.Lightning = &FakeLightning{}
	cfg.Warnings = &FakeWarnings{}
	cfg.Forecast = &FakeForecast{Data: Forecast{ThunderstormExpected: false}}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal: %+v", r.Level, r)
	}
}

func TestWatchFromForecast(t *testing.T) {
	cfg := baseConfig()
	cfg.Forecast = &FakeForecast{Data: Forecast{ThunderstormExpected: true}}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Watch {
		t.Fatalf("Level = %v, want Watch: %+v", r.Level, r)
	}
}

func TestWarningFromOfficialOrangeWarning(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Warnings = &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now.Add(-time.Hour), Expires: now.Add(time.Hour)},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Warning {
		t.Fatalf("Level = %v, want Warning: %+v", r.Level, r)
	}
}

func TestYellowWarningDoesNotEscalate(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Warnings = &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Thunderstorm", Color: "yellow", Onset: now.Add(-time.Hour), Expires: now.Add(time.Hour)},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal (yellow isn't orange/red): %+v", r.Level, r)
	}
}

func TestNonThunderstormWarningIgnored(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Warnings = &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Moderate Wind", Color: "orange", Onset: now.Add(-time.Hour), Expires: now.Add(time.Hour)},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal (wind warning isn't a thunderstorm): %+v", r.Level, r)
	}
}

func TestExpiredWarningIgnored(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Warnings = &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Thunderstorm", Color: "red", Onset: now.Add(-2 * time.Hour), Expires: now.Add(-time.Hour)},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal (warning already expired): %+v", r.Level, r)
	}
}

func TestWarningFromStrikeWithinWarningRadius(t *testing.T) {
	cfg := baseConfig() // warning radius 30km, danger 12km
	cfg.Lightning = &FakeLightning{StrikesData: []Strike{{At: time.Now(), Point: nearby(20)}}}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Warning {
		t.Fatalf("Level = %v, want Warning: %+v", r.Level, r)
	}
	if r.NearestStrikeKM < 19 || r.NearestStrikeKM > 21 {
		t.Errorf("NearestStrikeKM = %v, want ~20", r.NearestStrikeKM)
	}
}

func TestDangerFromStrikeWithinDangerRadius(t *testing.T) {
	cfg := baseConfig()
	cfg.Lightning = &FakeLightning{StrikesData: []Strike{{At: time.Now(), Point: nearby(5)}}}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Danger {
		t.Fatalf("Level = %v, want Danger: %+v", r.Level, r)
	}
}

func TestStrikeOutsideWarningRadiusIsNormal(t *testing.T) {
	cfg := baseConfig()
	cfg.Lightning = &FakeLightning{StrikesData: []Strike{{At: time.Now(), Point: nearby(50)}}}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal (strike well outside radius): %+v", r.Level, r)
	}
}

func TestLocalSensorAloneDoesNotConfirm(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Local = &FakeLocalSensor{Detects: []LocalDetection{{At: now}}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Normal || r.LocalConfirmed {
		t.Fatalf("a single, uncorroborated local detection must not confirm: %+v", r)
	}
}

func TestLocalSensorConfirmedByNetworkAgreement(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Local = &FakeLocalSensor{Detects: []LocalDetection{{At: now}}}
	cfg.Lightning = &FakeLightning{StrikesData: []Strike{{At: now, Point: nearby(5)}}} // within danger radius
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Danger || !r.LocalConfirmed {
		t.Fatalf("expected Danger with LocalConfirmed given network agreement: %+v", r)
	}
}

func TestLocalSensorConfirmedByTwoDetectionsWithinWindow(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Local = &FakeLocalSensor{Detects: []LocalDetection{
		{At: now.Add(-4 * time.Minute)},
		{At: now},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Danger || !r.LocalConfirmed {
		t.Fatalf("expected Danger from two local detections within 5 minutes: %+v", r)
	}
}

func TestLocalSensorTwoDetectionsOutsideWindowDoNotConfirm(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Local = &FakeLocalSensor{Detects: []LocalDetection{
		{At: now.Add(-20 * time.Minute)},
		{At: now},
	}}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Normal || r.LocalConfirmed {
		t.Fatalf("two detections 20 minutes apart must not confirm: %+v", r)
	}
}

func TestDangerBeatsWarningAndWatch(t *testing.T) {
	cfg := baseConfig()
	now := time.Now()
	cfg.Forecast = &FakeForecast{Data: Forecast{ThunderstormExpected: true}}
	cfg.Warnings = &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(time.Hour)},
	}}
	cfg.Lightning = &FakeLightning{StrikesData: []Strike{{At: now, Point: nearby(2)}}} // danger range
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Danger {
		t.Fatalf("Level = %v, want Danger (highest of all signals): %+v", r.Level, r)
	}
}

func TestSourceErrorsDoNotCrashEvaluation(t *testing.T) {
	cfg := baseConfig()
	cfg.Lightning = &FakeLightning{Err: errors.New("network down")}
	cfg.Warnings = &FakeWarnings{Err: errors.New("network down")}
	cfg.Forecast = &FakeForecast{Err: errors.New("network down")}
	cfg.Local = &FakeLocalSensor{Err: errors.New("i2c error")}
	m := NewMonitor(cfg, time.Now())

	r := m.Evaluate(context.Background(), time.Now())
	if r.Level != Normal {
		t.Fatalf("all sources erroring with nothing previously known should read Normal, got %v", r.Level)
	}
}

func TestStaleDuringNormalDoesNotEscalate(t *testing.T) {
	cfg := baseConfig()
	cfg.StaleAfter = 10 * time.Minute
	cfg.Forecast = &FakeForecast{Err: errors.New("down")} // never succeeds
	start := time.Now()
	m := NewMonitor(cfg, start)

	// Well past stale_after, with nothing else indicating a storm.
	later := start.Add(cfg.StaleAfter + time.Minute)
	r := m.Evaluate(context.Background(), later)
	if r.Level != Normal {
		t.Fatalf("Level = %v, want Normal (stale while nothing indicates a storm)", r.Level)
	}
	if !r.Stale {
		t.Error("expected Stale=true so the caller can still notify about it")
	}
}

func TestStaleEscalatesWarningToDanger(t *testing.T) {
	cfg := baseConfig()
	cfg.StaleAfter = 10 * time.Minute
	now := time.Now()

	// Warnings source succeeds once with an active orange warning...
	warn := &FakeWarnings{Warnings: []AlertWarning{
		{Event: "Thunderstorm", Color: "orange", Onset: now, Expires: now.Add(2 * time.Hour)},
	}}
	cfg.Warnings = warn
	// ...but the forecast source has never succeeded.
	cfg.Forecast = &FakeForecast{Err: errors.New("down")}
	m := NewMonitor(cfg, now)

	r := m.Evaluate(context.Background(), now)
	if r.Level != Warning {
		t.Fatalf("sanity check failed: Level = %v, want Warning before staleness kicks in", r.Level)
	}

	// Now advance past stale_after without the forecast source ever
	// succeeding: the whole evaluation must be treated as Danger.
	later := now.Add(cfg.StaleAfter + time.Minute)
	r = m.Evaluate(context.Background(), later)
	if r.Level != Danger {
		t.Fatalf("Level = %v, want Danger (Warning + stale source escalates)", r.Level)
	}
	if !r.Stale {
		t.Error("expected Stale=true")
	}
}

func TestNewMonitorGrantsStartupGracePeriodBeforeStale(t *testing.T) {
	cfg := baseConfig()
	cfg.StaleAfter = 10 * time.Minute
	cfg.Forecast = &FakeForecast{Err: errors.New("down")} // never succeeds
	start := time.Now()
	m := NewMonitor(cfg, start)

	// Immediately after construction, a source that hasn't had a chance
	// to succeed yet must not already read as stale.
	r := m.Evaluate(context.Background(), start)
	if r.Stale {
		t.Fatalf("expected no staleness immediately after construction, got stale sources %v", r.StaleSources)
	}
}

func TestResultStringDoesNotPanic(t *testing.T) {
	cfg := baseConfig()
	m := NewMonitor(cfg, time.Now())
	r := m.Evaluate(context.Background(), time.Now())
	if r.String() == "" {
		t.Error("expected a non-empty summary string")
	}
}
