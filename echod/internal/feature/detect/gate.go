package detect

import (
	"log/slog"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// newGateEntities are the other-room floor's two controls, on the microphone beside the stop word:
// a switch that makes the floor act, and the floor itself in dBFS before leveling. With the switch
// off the floor is still measured against and logged, so it can be checked on real wakes before it
// is trusted to drop any. The engine asks both at every crossing, so neither needs a reload.
func newGateEntities() (*esphome.Switch, *esphome.Number) {
	on := &esphome.Switch{
		Base: esphome.Base{
			ObjectID: "other_room_gate",
			DeviceID: component.DeviceMicrophone,
			Name:     "Ignore other rooms",
			Icon:     "mdi:door-closed",
			Category: esphome.CategoryConfig,
		},
	}
	on.OnCommand = func(v bool) {
		on.Set(v)
		if err := config.Set().Gate().On(v); err != nil {
			slog.Error("saving the other-room floor failed", "err", err)
		}
		slog.Info("other-room floor", "on", v, "db", config.Get().Wake.Gate.Level())
	}

	db := &esphome.Number{
		Base: esphome.Base{
			ObjectID: "other_room_floor",
			DeviceID: component.DeviceMicrophone,
			Name:     "Other-room floor",
			Icon:     "mdi:volume-low",
			Category: esphome.CategoryConfig,
		},
		// Below -80 nothing real is that quiet; above -30 a word said to the device's face would be
		// ignored. The default sits at -56 (config.DefaultGateDB).
		Min: -80, Max: -30, Step: 0.5, Unit: "dBFS",
		Mode: esphome.NumberBox,
	}
	db.OnCommand = func(v float32) {
		db.Set(v)
		if err := config.Set().Gate().DB(float64(v)); err != nil {
			slog.Error("saving the other-room floor failed", "err", err)
		}
		slog.Info("other-room floor", "on", config.Get().Wake.Gate.On, "db", v)
	}
	return on, db
}
