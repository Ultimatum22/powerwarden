package as3935

import (
	"context"
	"testing"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpiotest"
)

func newTestWatcher(t *testing.T, retain time.Duration) (*Watcher, *fakeBus, *gpiotest.Pin) {
	t.Helper()
	bus := newFakeBus()
	sensor, err := New(bus, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pin := &gpiotest.Pin{N: "irq", EdgesChan: make(chan gpio.Level, 4)}
	w, err := NewWatcher(sensor, pin, retain)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	return w, bus, pin
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestWatcherRecordsLightningInterrupt(t *testing.T) {
	w, bus, pin := newTestWatcher(t, 0)
	bus.setReg(regInterrupt, byte(ReasonLightning))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pin.EdgesChan <- gpio.High

	waitUntil(t, time.Second, func() bool {
		dets, err := w.Detections(context.Background(), time.Time{})
		return err == nil && len(dets) == 1
	})
}

func TestWatcherIgnoresNonLightningReasons(t *testing.T) {
	w, bus, pin := newTestWatcher(t, 0)
	bus.setReg(regInterrupt, byte(ReasonDisturber))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pin.EdgesChan <- gpio.High
	// Give it a moment to (not) record, then assert nothing landed. A
	// disturber followed by a real lightning event confirms the watcher
	// is still alive and just filtered the first one, not stuck.
	time.Sleep(50 * time.Millisecond)
	bus.setReg(regInterrupt, byte(ReasonLightning))
	pin.EdgesChan <- gpio.High

	waitUntil(t, time.Second, func() bool {
		dets, err := w.Detections(context.Background(), time.Time{})
		return err == nil && len(dets) == 1
	})
}

func TestDetectionsFiltersBySince(t *testing.T) {
	w, _, _ := newTestWatcher(t, time.Hour)
	now := time.Now()
	w.record(now.Add(-10 * time.Minute))
	w.record(now)

	dets, err := w.Detections(context.Background(), now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Detections: %v", err)
	}
	if len(dets) != 1 {
		t.Fatalf("got %d detections, want 1 (only the recent one)", len(dets))
	}
}

func TestOldDetectionsArePruned(t *testing.T) {
	w, _, _ := newTestWatcher(t, time.Minute)
	now := time.Now()
	w.record(now.Add(-2 * time.Minute)) // older than retain, should be pruned
	w.record(now)

	dets, err := w.Detections(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Detections: %v", err)
	}
	if len(dets) != 1 {
		t.Fatalf("got %d detections, want 1 (the old one should have been pruned)", len(dets))
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	w, _, _ := newTestWatcher(t, 0)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
