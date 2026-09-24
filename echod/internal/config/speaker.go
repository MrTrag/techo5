package config

// Speaker is how loud the device is, how voice is stretched to the playback rate, and whether the
// driver's tuning is applied.
type Speaker struct {
	Volume     int        `json:"volume"`
	Resampling Resampling `json:"resampling"`
	ASP        bool       `json:"asp"`

	// AmpStandby rests the speaker amplifier a few seconds after the last sound, as Android's audio
	// HAL does, and wakes it just before the next: see hardware/speaker. Off by default: it silences
	// the LED ring's bleed into the amplifier while the device thinks, but the amplifier coming off
	// is an audible pop, and Steve chose no ring during a turn over the pop (24 Sep 2026).
	AmpStandby bool `json:"amp_standby"`
}

const (
	// VolumeSteps runs 0..30, the range Android gives STREAM_MUSIC and the one the vendor's volume
	// curves are indexed by, so a step here is a step there. Home Assistant works in 0..1.
	VolumeSteps = 30

	// Half way up, so a device nobody has turned up is audible without being startling.
	DefaultVolume = VolumeSteps / 2

	DefaultResampling = ResampleSinc

	// DefaultASP applies the driver's tuning, which is what the vendor's firmware does.
	DefaultASP = true
)

func defaultSpeaker() Speaker {
	return Speaker{Volume: DefaultVolume, Resampling: DefaultResampling, ASP: DefaultASP}
}

type SpeakerWriter struct{ st *Store }

func (w SpeakerWriter) Volume(v int) error {
	return w.st.Update(func(c *Config) { c.Speaker.Volume = v })
}

func (w SpeakerWriter) Resampling(v Resampling) error {
	return w.st.Update(func(c *Config) { c.Speaker.Resampling = v })
}

func (w SpeakerWriter) ASP(v bool) error {
	return w.st.Update(func(c *Config) { c.Speaker.ASP = v })
}

// Standby records whether the amplifier rests between sounds.
func (w SpeakerWriter) Standby(v bool) error {
	return w.st.Update(func(c *Config) { c.Speaker.AmpStandby = v })
}

// Resampling is how the 16 kHz voice a pipeline sends is stretched to the 48 kHz the codec takes.
type Resampling string

const (
	// ResampleSinc interpolates through a low pass at the input's Nyquist, which is the correct
	// answer and what the device uses unless told otherwise.
	ResampleSinc Resampling = "sinc"

	// ResampleLinear draws a straight line between input samples. It attenuates the images rather
	// than removing them, for a fraction of the work.
	ResampleLinear Resampling = "linear"

	// ResampleHold repeats each input sample, which is what the device did before any of this and
	// leaves the images in full.
	ResampleHold Resampling = "hold"
)

// Label is how the setting is shown.
func (r Resampling) Label() string {
	switch r {
	case ResampleSinc:
		return "Band limited"
	case ResampleLinear:
		return "Linear"
	case ResampleHold:
		return "Repeat samples"
	}
	return string(r)
}
