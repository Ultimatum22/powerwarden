package as3935

import (
	"fmt"
	"sync"
	"testing"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
)

// fakeBus is a minimal in-memory I2C register map, standing in for real
// hardware per CLAUDE.md's testing rules ("never call... in tests" applies
// equally to real hardware, not just network services).
type fakeBus struct {
	mu   sync.Mutex
	regs map[byte]byte
}

func newFakeBus() *fakeBus { return &fakeBus{regs: map[byte]byte{}} }

func (b *fakeBus) String() string                  { return "fakeBus" }
func (b *fakeBus) SetSpeed(physic.Frequency) error { return nil }
func (b *fakeBus) reg(r byte) byte                 { b.mu.Lock(); defer b.mu.Unlock(); return b.regs[r] }
func (b *fakeBus) setReg(r, v byte)                { b.mu.Lock(); defer b.mu.Unlock(); b.regs[r] = v }

func (b *fakeBus) Tx(addr uint16, w, r []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case len(w) == 2 && len(r) == 0: // register write
		b.regs[w[0]] = w[1]
		return nil
	case len(w) == 1 && len(r) == 1: // register read
		r[0] = b.regs[w[0]]
		return nil
	default:
		return fmt.Errorf("fakeBus: unsupported Tx w=%v r=%v", w, r)
	}
}

var _ i2c.Bus = (*fakeBus)(nil)

func TestNewResetsAndCalibrates(t *testing.T) {
	bus := newFakeBus()
	if _, err := New(bus, 0); err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := bus.reg(regPresetDefault); got != presetDefaultValue {
		t.Errorf("PRESET_DEFAULT register = 0x%02X, want 0x%02X", got, presetDefaultValue)
	}
	if got := bus.reg(regCalibRCO); got != calibRCOValue {
		t.Errorf("CALIB_RCO register = 0x%02X, want 0x%02X", got, calibRCOValue)
	}
}

func TestNewDefaultsAddress(t *testing.T) {
	bus := newFakeBus()
	s, err := New(bus, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.dev.Addr != DefaultAddress {
		t.Errorf("Addr = 0x%02X, want default 0x%02X", s.dev.Addr, DefaultAddress)
	}
}

func TestInterruptReasonMasksToLowNibble(t *testing.T) {
	bus := newFakeBus()
	s, err := New(bus, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bus.setReg(regInterrupt, 0xF8) // high nibble garbage + lightning (0x08)
	reason, err := s.InterruptReason()
	if err != nil {
		t.Fatalf("InterruptReason: %v", err)
	}
	if reason != ReasonLightning {
		t.Errorf("InterruptReason = 0x%02X, want ReasonLightning", reason)
	}
}

func TestDistanceKMMasksOutOfRange(t *testing.T) {
	bus := newFakeBus()
	s, err := New(bus, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bus.setReg(regDistance, 0xFF) // upper bits garbage, lower 6 bits = 0x3F
	d, err := s.DistanceKM()
	if err != nil {
		t.Fatalf("DistanceKM: %v", err)
	}
	if d != OutOfRange {
		t.Errorf("DistanceKM = %d, want OutOfRange (%d)", d, OutOfRange)
	}
}
