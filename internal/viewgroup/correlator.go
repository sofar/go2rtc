package viewgroup

import (
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/inference"
)

// Correlator watches detection events for a group and correlates
// detections of the same class across different cameras within the
// correlation window.
type Correlator struct {
	group *Group
	bus   *events.Bus
	stop  chan struct{}

	mu      sync.Mutex
	pending []*pendingDetection
}

// NewCorrelator creates a correlator for a view group.
func NewCorrelator(group *Group, bus *events.Bus) *Correlator {
	return &Correlator{
		group: group,
		bus:   bus,
		stop:  make(chan struct{}),
	}
}

// Start begins listening for detection events and running the
// correlation/expiry loop.
func (c *Correlator) Start() {
	// Subscribe to detection events for cameras in this group
	ch := c.bus.Subscribe(events.Filter{Type: events.TypeDetection}, 128)

	// Event processing goroutine
	go func() {
		for {
			select {
			case <-c.stop:
				c.bus.Unsubscribe(ch)
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				c.handleEvent(e)
			}
		}
	}()

	// Expiry goroutine — flushes pending detections past the window
	go func() {
		ticker := time.NewTicker(c.group.CorrelationWindow / 2)
		defer ticker.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				c.flushExpired()
			}
		}
	}()
}

// Stop terminates the correlator.
func (c *Correlator) Stop() {
	close(c.stop)
}

func (c *Correlator) handleEvent(e *events.Event) {
	// Only process events for cameras in this group
	if !c.inGroup(e.Camera) {
		return
	}

	de, ok := e.Data.(inference.DetectionEvent)
	if !ok {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	for _, det := range de.Detections {
		pd := &pendingDetection{
			camera:    e.Camera,
			class:     det.Class,
			timestamp: now,
			event:     de,
		}

		// Check for correlation with existing pending detections
		correlated := false
		for _, existing := range c.pending {
			if existing.class == det.Class &&
				existing.camera != e.Camera &&
				now.Sub(existing.timestamp) <= c.group.CorrelationWindow {
				// Correlated — same class, different camera, within window
				existing.confirmed = true
				pd.confirmed = true
				correlated = true

				c.emitCorrelated(det.Class, []string{existing.camera, e.Camera})
			}
		}

		// If require_multi_angle is false, emit single-camera detections
		// immediately. They may still get correlated later within the window.
		if !correlated && !c.group.RequireMultiAngle {
			// Will be emitted on expiry if still uncorrelated
		}

		c.pending = append(c.pending, pd)
	}
}

// flushExpired removes pending detections older than the correlation
// window and emits uncorrelated events if require_multi_angle is false.
func (c *Correlator) flushExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-c.group.CorrelationWindow)
	var kept []*pendingDetection

	for _, pd := range c.pending {
		if pd.timestamp.Before(cutoff) {
			// Expired — emit if uncorrelated and not requiring multi-angle
			if !pd.confirmed && !c.group.RequireMultiAngle {
				c.emitUncorrelated(pd)
			}
			continue
		}
		kept = append(kept, pd)
	}

	c.pending = kept
}

func (c *Correlator) emitCorrelated(class string, cameras []string) {
	c.bus.Publish(&events.Event{
		Type: "group_detection",
		Data: GroupDetectionEvent{
			Group:      c.group.Name,
			Class:      class,
			Cameras:    cameras,
			Correlated: true,
		},
	})

	log.Info().
		Str("group", c.group.Name).
		Str("class", class).
		Strs("cameras", cameras).
		Msg("[viewgroup] correlated detection")
}

func (c *Correlator) emitUncorrelated(pd *pendingDetection) {
	c.bus.Publish(&events.Event{
		Type:   "group_detection",
		Camera: pd.camera,
		Data: GroupDetectionEvent{
			Group:      c.group.Name,
			Class:      pd.class,
			Cameras:    []string{pd.camera},
			Correlated: false,
		},
	})
}

func (c *Correlator) inGroup(camera string) bool {
	for _, cam := range c.group.Cameras {
		if cam == camera {
			return true
		}
	}
	return false
}
