package clock

import (
	"testing"
	"time"
)

func TestFakeAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFake(start)
	if !f.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", f.Now(), start)
	}
	if !f.Trusted() {
		t.Fatal("expected fake clock to be trusted by default")
	}
	got := f.Advance(30 * time.Second)
	want := start.Add(30 * time.Second)
	if !got.Equal(want) || !f.Now().Equal(want) {
		t.Fatalf("after Advance: got %v, want %v", f.Now(), want)
	}
}

func TestFakeSetTrusted(t *testing.T) {
	f := NewFake(time.Now())
	f.SetTrusted(false)
	if f.Trusted() {
		t.Fatal("expected Trusted() to be false after SetTrusted(false)")
	}
}

func TestRealClockTrustedForPlausibleTime(t *testing.T) {
	if !(Real{}).Trusted() {
		t.Fatal("expected the real clock to be trusted when the system time is plausible")
	}
}
