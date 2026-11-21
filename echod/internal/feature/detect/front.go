package detect

import (
	"log/slog"
	"sync/atomic"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// exactHA is whether Home Assistant's wake words run on the exact front end on this device
// (config.Wake.ExactHA). Read by exactFor at every load; off until restored or switched.
var exactHA atomic.Bool

// newFrontEntity is the switch for it, on the microphone beside the other-room floor. Off, the
// default, a word from Home Assistant runs on the approximate front end as it always has here;
// on, it moves to the exact one at once, without a restart (Engine.Refront). It is per device so a
// round trained for the exact front end can be tried on one Dot while the rest stay as they are.
func newFrontEntity(e *Engine) *esphome.Switch {
	sw := &esphome.Switch{
		Base: esphome.Base{
			ObjectID: "exact_front_end_ha",
			DeviceID: component.DeviceMicrophone,
			Name:     "Exact front end for Home Assistant words",
			Icon:     "mdi:waveform",
			Category: esphome.CategoryConfig,
		},
	}
	sw.OnCommand = func(v bool) {
		sw.Set(v)
		if err := config.Set().Front().ExactHA(v); err != nil {
			slog.Error("saving the front end choice failed", "err", err)
		}
		if exactHA.Swap(v) != v {
			e.Refront()
		}
		slog.Info("front end for Home Assistant words", "exact", v)
	}
	return sw
}
