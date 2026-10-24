# The Jarvis build of TECHO5

Thanks to HuskerMinion and everyone who has contributed to [TECHO5](https://github.com/HuskerMinion/techo5),
and to the projects it stands on: EchoLocal, the amonet unlock, microWakeWord and openWakeWord. TECHO5
turned four old Echo Dot 2nd gens in our house into local Home Assistant voice satellites, with no Amazon
account and nothing leaving the house, and it has been brilliant.

This `jarvis` branch is the build we run on those four Dots. It is TECHO5 as released, with a few things
added on top, mostly for training and tuning a "Jarvis" wake word of our own. It is shared in case any of
it is useful to someone, not as a replacement: if you are not sure, use the official releases. Everything
here has only been tested on the Dot 2nd gen.

For TECHO5 itself, how to install it and everything it does, see
[TECHO5's own readme](https://github.com/HuskerMinion/techo5#readme).

## What it adds

- **Two-stage wake word models and a desk scorer.** The model interpreter runs the comparison operators
  and IF, so an openWakeWord classifier with a verifier stage behind it loads and scores on the device.
  `cmd/wakescore` replays WAV files through the same engine and judge the Dot uses and reports what a Dot
  would have done, so a model can be tried on a PC before it goes near a room.
- **Amplifier standby.** The speaker can rest its amplifier a few seconds after the last sound, which
  silences the LED ring's bleed through the speaker while the device is thinking. Listening and replying
  still carry it, and the amplifier turning off is a small audible pop. Off by default, switchable from
  Home Assistant.
- **Recordings that start before the wake word.** The microphone keeps two seconds, taken at the moment
  of detection, so a kept recording holds the whole word and not just its tail.
- **Shadow mode.** The second wake word slot can be watched but never acted on: it opens no turn and
  makes no sound, and keeps a clip with its score whenever it would have fired. A new model can be judged
  in the real rooms, beside the live one, before anything changes.
- **A score trace.** At every crossing the last two seconds of scores are logged frame by frame and saved
  in the clip's sidecar, so the Dot's curve can be laid beside the desk scorer's for the same audio.
- **Loudness on every wake.** Each wake logs how loud the word arrived, before and after levelling, and
  the clips carry the same figures, TECHO5's own Keep wake clips included.
- **Ignore other rooms.** In a house where talking carries, every Dot hears every wake word. With "Ignore
  other rooms" on, a word that arrives quieter than the "Other-room floor" (default -56 dBFS) is treated
  as said in another room and nothing happens. Off by default, and while off it is only logged.
- **More recordings, and training sessions.** Each assistant keeps up to 200 recordings, up from 10, and
  a training session started from Home Assistant records a few minutes of a room on purpose, to catch the
  tries that never came close.
- **A "Done" sound.** When Home Assistant's reply is the single word "Done", a `done.wav` of your own
  plays straight away in its place.
- **A named wake sound.** The wake sound can say the person's name ("Yes, Steve?") from
  `wake_<name>.wav`, when a voice-ID service on the network says who spoke.

## Before trying it

- **The voice check is ours.** The named wake sound asks a speaker-recognition service we run on our own
  server, and the default address is that server's. Without an answer a wake waits up to 250 ms and then
  plays the ordinary sound. Setting `voice_check_url` to `off` in the wake settings turns it off.
- **The wake word models are not included.** Ours are trained on our own voices. The stock models, and
  any microWakeWord model of your own, work as usual.
- **How it is kept up to date.** It is merged with each TECHO5 release, the latest being dot-v1.0.32.
  Where TECHO5 does something as well as or better than an addition here, we use TECHO5's and drop ours.
- **Problems.** If something goes wrong on this build, please raise it here rather than on TECHO5, since
  it may well be one of our changes.

Happy to answer questions, or to tidy any of this into a pull request if HuskerMinion would like it.
