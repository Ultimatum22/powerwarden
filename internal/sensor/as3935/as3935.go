// Package as3935 drives the AS3935 lightning sensor over I2C, per its
// datasheet's register map. It's the local, low-latency corroborating
// source in internal/weather's threat level: CLAUDE.md notes the sensor is
// noisy, so a single detection only counts if another source agrees or a
// second local detection follows within a short window — that filtering
// lives in internal/weather, not here; this package only reports raw
// interrupt events.
package as3935

import (
	"fmt"

	"periph.io/x/conn/v3/i2c"
)

// DefaultAddress is the AS3935's default I2C address (also CLAUDE.md's
// example config value).
const DefaultAddress = 0x03

const (
	regInterrupt     = 0x03
	regDistance      = 0x07
	regPresetDefault = 0x3C
	regCalibRCO      = 0x3D

	presetDefaultValue = 0x96 // datasheet: write to REG 0x3C to reset to defaults
	calibRCOValue      = 0x96 // datasheet: write to REG 0x3D to calibrate the RC oscillators
)

// InterruptReason is the AS3935's INT register value (low nibble) after it
// asserts its interrupt pin.
type InterruptReason byte

const (
	ReasonNone      InterruptReason = 0x00
	ReasonNoiseHigh InterruptReason = 0x01
	ReasonDisturber InterruptReason = 0x04
	ReasonLightning InterruptReason = 0x08
)

// OutOfRange is the DistanceKM value the sensor reports when it can't
// estimate the storm's distance.
const OutOfRange = 0x3F

// Sensor is a thin register-level driver: reset, calibrate, and read the
// interrupt reason / distance estimate. Interrupt-pin handling and event
// buffering live in Watcher.
type Sensor struct {
	dev *i2c.Dev
}

// New opens the sensor on bus at addr (DefaultAddress if zero), resets it
// to power-on defaults, and calibrates its internal RC oscillators, per
// the datasheet's recommended startup sequence.
func New(bus i2c.Bus, addr uint16) (*Sensor, error) {
	if addr == 0 {
		addr = DefaultAddress
	}
	s := &Sensor{dev: &i2c.Dev{Bus: bus, Addr: addr}}
	if err := s.writeReg(regPresetDefault, presetDefaultValue); err != nil {
		return nil, fmt.Errorf("as3935: reset to defaults: %w", err)
	}
	if err := s.writeReg(regCalibRCO, calibRCOValue); err != nil {
		return nil, fmt.Errorf("as3935: calibrate RC oscillators: %w", err)
	}
	return s, nil
}

func (s *Sensor) readReg(reg byte) (byte, error) {
	var buf [1]byte
	if err := s.dev.Tx([]byte{reg}, buf[:]); err != nil {
		return 0, fmt.Errorf("as3935: read register 0x%02X: %w", reg, err)
	}
	return buf[0], nil
}

func (s *Sensor) writeReg(reg, value byte) error {
	if err := s.dev.Tx([]byte{reg, value}, nil); err != nil {
		return fmt.Errorf("as3935: write register 0x%02X: %w", reg, err)
	}
	return nil
}

// InterruptReason reads why the sensor asserted its interrupt pin. Per the
// datasheet, callers should wait about 2ms after the interrupt fires
// before calling this (Watcher does).
func (s *Sensor) InterruptReason() (InterruptReason, error) {
	v, err := s.readReg(regInterrupt)
	if err != nil {
		return 0, err
	}
	return InterruptReason(v & 0x0F), nil
}

// DistanceKM reads the sensor's own storm-distance estimate. OutOfRange
// means it couldn't estimate one.
func (s *Sensor) DistanceKM() (int, error) {
	v, err := s.readReg(regDistance)
	if err != nil {
		return 0, err
	}
	return int(v & 0x3F), nil
}
