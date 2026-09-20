package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"

	"github.com/Ultimatum22/powerwarden/internal/config"
	"github.com/Ultimatum22/powerwarden/internal/sensor/as3935"
	"github.com/Ultimatum22/powerwarden/internal/weather"
)

// newWeatherMonitorConfig builds a weather.Config from the validated
// config, wiring in whichever real sources are enabled. It never fails
// outright: a source that can't be built (e.g. no AS3935 physically
// present) is logged and left disabled, since weather ships notify-only —
// labpower's core scheduling must keep running either way.
func newWeatherMonitorConfig(cfg *config.Config, logger *slog.Logger) weather.Config {
	wc := weather.Config{
		Location:                 weather.Point{Lat: cfg.Weather.Location.Lat, Lon: cfg.Weather.Location.Lon},
		WarningRadiusKM:          cfg.Weather.Levels.Warning.StrikeRadiusKM,
		WarningCountdown:         cfg.Weather.Levels.Warning.Countdown,
		DangerRadiusKM:           cfg.Weather.Levels.Danger.StrikeRadiusKM,
		AllClearAfter:            cfg.Weather.Levels.AllClearAfter,
		StaleAfter:               cfg.Weather.StaleAfter,
		LocalCorroborationWindow: cfg.Weather.LocalSensor.CorroborationWindow,
	}

	if cfg.Weather.Forecast.Provider == "open-meteo" {
		wc.Forecast = weather.OpenMeteo{Location: wc.Location}
	}
	if cfg.Weather.Warnings.Provider == "meteoalarm" {
		wc.Warnings = weather.MeteoAlarm{Country: cfg.Weather.Warnings.Region}
	}
	if cfg.Weather.LightningNetwork.Enabled {
		// No real-time lightning network client is wired in: Blitzortung,
		// the network CLAUDE.md names as the example, has no public API
		// for third-party use (data access is restricted to registered
		// station operators). Danger level is still reachable via two
		// local AS3935 detections within the corroboration window.
		logger.Warn("weather: lightning_network.enabled is true but no lightning network client is configured; Danger level will only come from the local sensor or official warnings")
	}
	if cfg.Weather.LocalSensor.Enabled {
		watcher, err := newAS3935Watcher(cfg, logger)
		if err != nil {
			logger.Error("weather: local sensor unavailable, continuing without it", "error", err)
		} else {
			wc.Local = watcher
		}
	}

	return wc
}

// newAS3935Watcher opens the configured I2C bus and IRQ GPIO pin and
// starts watching for lightning interrupts. The caller is responsible for
// running watcher.Run(ctx) in its own goroutine.
func newAS3935Watcher(cfg *config.Config, logger *slog.Logger) (*as3935.Watcher, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("init periph host drivers: %w", err)
	}

	busName := i2cBusName(cfg.Weather.LocalSensor.Bus)
	bus, err := i2creg.Open(busName)
	if err != nil {
		return nil, fmt.Errorf("open i2c bus %q (from %q): %w", busName, cfg.Weather.LocalSensor.Bus, err)
	}

	addr := uint16(cfg.Weather.LocalSensor.Address)
	if addr == 0 {
		addr = as3935.DefaultAddress
	}
	sensor, err := as3935.New(bus, addr)
	if err != nil {
		return nil, fmt.Errorf("init as3935 sensor: %w", err)
	}

	pinName := fmt.Sprintf("GPIO%d", cfg.Weather.LocalSensor.IRQGPIO)
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		return nil, fmt.Errorf("gpio pin %q not found", pinName)
	}
	pinIn, ok := pin.(gpio.PinIn)
	if !ok {
		return nil, fmt.Errorf("gpio pin %q does not support input mode", pinName)
	}

	watcher, err := as3935.NewWatcher(sensor, pinIn, 0)
	if err != nil {
		return nil, fmt.Errorf("configure irq pin: %w", err)
	}
	logger.Info("weather: AS3935 local sensor ready", "bus", busName, "addr", addr, "irq_pin", pinName)
	return watcher, nil
}

// i2cBusName turns a device path like "/dev/i2c-1" into the bus number
// periph.io's i2creg expects ("1"); anything it can't parse that way is
// passed through as-is, and "" asks i2creg.Open for the system default.
func i2cBusName(devPath string) string {
	if devPath == "" {
		return ""
	}
	if i := strings.LastIndex(devPath, "-"); i != -1 {
		if _, err := strconv.Atoi(devPath[i+1:]); err == nil {
			return devPath[i+1:]
		}
	}
	return devPath
}
