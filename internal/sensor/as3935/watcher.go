package as3935

import (
	"context"
	"sync"
	"time"

	"periph.io/x/conn/v3/gpio"

	"github.com/Ultimatum22/powerwarden/internal/weather"
)

// interruptSettleDelay is the datasheet's recommended pause between the
// interrupt pin firing and reading the interrupt reason register.
const interruptSettleDelay = 2 * time.Millisecond

// Watcher turns the sensor's interrupt pin into a weather.LocalSensor,
// buffering recent lightning-reason detections in memory so Detections
// can answer "since T" without touching the hardware again.
type Watcher struct {
	sensor *Sensor
	irq    gpio.PinIn
	now    func() time.Time
	retain time.Duration

	mu         sync.Mutex
	detections []weather.LocalDetection
}

// NewWatcher configures irq as a pulled-down, rising-edge input and
// returns a Watcher ready to have Run called on it. retain controls how
// long detections are kept in memory (zero uses 30 minutes, comfortably
// longer than any corroboration window internal/weather uses).
func NewWatcher(sensor *Sensor, irq gpio.PinIn, retain time.Duration) (*Watcher, error) {
	if err := irq.In(gpio.PullDown, gpio.RisingEdge); err != nil {
		return nil, err
	}
	if retain <= 0 {
		retain = 30 * time.Minute
	}
	return &Watcher{sensor: sensor, irq: irq, now: time.Now, retain: retain}, nil
}

// Run watches for interrupts until ctx is done. Call it in its own
// goroutine; it returns once ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if !w.irq.WaitForEdge(time.Second) {
			continue // timeout; loop back and check ctx again
		}
		time.Sleep(interruptSettleDelay)
		reason, err := w.sensor.InterruptReason()
		if err != nil {
			continue // transient I2C error; the next interrupt will retry
		}
		if reason == ReasonLightning {
			w.record(w.now())
		}
	}
}

func (w *Watcher) record(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.detections = append(w.detections, weather.LocalDetection{At: at})

	cutoff := at.Add(-w.retain)
	i := 0
	for i < len(w.detections) && w.detections[i].At.Before(cutoff) {
		i++
	}
	w.detections = w.detections[i:]
}

// Detections implements weather.LocalSensor.
func (w *Watcher) Detections(_ context.Context, since time.Time) ([]weather.LocalDetection, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []weather.LocalDetection
	for _, d := range w.detections {
		if !d.At.Before(since) {
			out = append(out, d)
		}
	}
	return out, nil
}

var _ weather.LocalSensor = (*Watcher)(nil)
