package weather

import (
	"context"
	"time"
)

// FakeLightning is a settable LightningSource for tests.
type FakeLightning struct {
	StrikesData []Strike
	Err         error
}

func (f *FakeLightning) Strikes(_ context.Context, box BoundingBox) ([]Strike, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	var out []Strike
	for _, s := range f.StrikesData {
		if box.Contains(s.Point) {
			out = append(out, s)
		}
	}
	return out, nil
}

// FakeWarnings is a settable WarningSource for tests.
type FakeWarnings struct {
	Warnings []AlertWarning
	Err      error
}

func (f *FakeWarnings) ActiveWarnings(_ context.Context) ([]AlertWarning, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Warnings, nil
}

// FakeForecast is a settable ForecastSource for tests.
type FakeForecast struct {
	Data Forecast
	Err  error
}

func (f *FakeForecast) Forecast(_ context.Context) (Forecast, error) {
	if f.Err != nil {
		return Forecast{}, f.Err
	}
	return f.Data, nil
}

// FakeLocalSensor is a settable LocalSensor for tests.
type FakeLocalSensor struct {
	Detects []LocalDetection
	Err     error
}

func (f *FakeLocalSensor) Detections(_ context.Context, since time.Time) ([]LocalDetection, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	var out []LocalDetection
	for _, d := range f.Detects {
		if !d.At.Before(since) {
			out = append(out, d)
		}
	}
	return out, nil
}

var (
	_ LightningSource = (*FakeLightning)(nil)
	_ WarningSource   = (*FakeWarnings)(nil)
	_ ForecastSource  = (*FakeForecast)(nil)
	_ LocalSensor     = (*FakeLocalSensor)(nil)
)
