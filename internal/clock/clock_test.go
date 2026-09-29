package clock

import (
	"os"
	"path/filepath"
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

func TestRealAnyPlausibleSkipsEvidence(t *testing.T) {
	dir := t.TempDir()
	c := Real{SyncMarker: filepath.Join(dir, "none"), RTCHCToSys: filepath.Join(dir, "none"), AnyPlausible: true}
	if !c.Trusted() {
		t.Fatal("AnyPlausible should trust a plausible system time without evidence")
	}
}

func TestRealTrustedNeedsSyncOrRTC(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "synchronized")
	hctosys := filepath.Join(dir, "hctosys")
	write := func(path, v string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := Real{SyncMarker: marker, RTCHCToSys: hctosys}

	if c.Trusted() {
		t.Fatal("trusted with neither NTP sync nor RTC")
	}
	write(hctosys, "0\n") // e.g. an RTC that didn't set the clock at boot
	if c.Trusted() {
		t.Fatal("trusted with hctosys=0")
	}
	write(hctosys, "1\n")
	if !c.Trusted() {
		t.Fatal("not trusted although the clock was set from the RTC")
	}
	if err := os.Remove(hctosys); err != nil {
		t.Fatal(err)
	}
	write(marker, "")
	if !c.Trusted() {
		t.Fatal("not trusted although timesyncd reports synchronised")
	}
}
