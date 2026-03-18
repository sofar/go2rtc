// Package viewgroup correlates object detections across cameras that
// view the same physical area. When multiple cameras detect the same
// object class within a configurable time window, the detection is
// marked as "correlated" (higher confidence). Optionally, single-camera
// detections can be suppressed to reduce false positives.
package viewgroup

import (
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

var correlators []*Correlator

func Init() {
	log = app.GetLogger("viewgroup")

	var cfg struct {
		Mod map[string]*GroupConfig `yaml:"view_groups"`
	}
	app.LoadConfig(&cfg)

	if len(cfg.Mod) == 0 {
		return
	}

	bus := events.Default

	for name, gc := range cfg.Mod {
		window, err := time.ParseDuration(gc.CorrelationWindow)
		if err != nil || window <= 0 {
			window = 3 * time.Second
		}

		group := &Group{
			Name:              name,
			Cameras:           gc.Cameras,
			CorrelationWindow: window,
			RequireMultiAngle: gc.RequireMultiAngle,
		}

		c := NewCorrelator(group, bus)
		c.Start()
		correlators = append(correlators, c)

		log.Info().
			Str("group", name).
			Strs("cameras", gc.Cameras).
			Dur("window", window).
			Bool("require_multi", gc.RequireMultiAngle).
			Msg("[viewgroup] started")
	}
}
