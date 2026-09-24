#!/usr/bin/env python3
"""Produce the two-stage classifier fixture using TensorFlow Lite's own runtime.

A two-stage openWakeWord classifier routes through an IF: the first network scores every window,
and only a score of 0.5 or more runs the verifier network, whose score then replaces it. To prove
both branches, the fixture holds two cases. "quiet" is referenceEmbeddings(), which the first
stage rejects; "loud" is a window found here by climbing the first stage's score until it trips.

    pip install ai-edge-litert          # or tensorflow, for tf.lite.Interpreter
    python3 generate_if_reference.py /path/to/hey_jarvis_v0.1.tflite   # named after the model
"""

import math
import os
import sys

import numpy as np

try:
    from ai_edge_litert.interpreter import Interpreter
except ImportError:
    from tensorflow.lite import Interpreter


def embeddings():
    """referenceEmbeddings: 16 frames of 96, as generate_reference.py makes them."""
    out = np.empty(16 * 96, dtype=np.float32)
    for i in range(16 * 96):
        out[i] = 0.6 * math.sin(i * 0.037) + 0.4 * math.cos(i * 0.011) - 0.3 * math.sin(i * 0.9)
    return out


def main():
    model = sys.argv[1]
    stem = os.path.basename(model).removesuffix(".tflite")
    out_name = f"twostage_{stem}_reference.txt"

    interpreter = Interpreter(model_path=model, experimental_preserve_all_tensors=True)
    interpreter.allocate_tensors()
    spec = interpreter.get_input_details()[0]
    out_spec = interpreter.get_output_details()[0]

    # The first stage's score is whatever feeds the main graph's comparison.
    ops = interpreter._get_ops_details()
    gates = [o for o in ops if o["op_name"] == "GREATER_EQUAL"]
    if len(gates) != 1:
        sys.exit(f"{model}: expected one GREATER_EQUAL in the main graph, found {len(gates)}")
    stage1 = int(gates[0]["inputs"][0])

    def run(x):
        interpreter.set_tensor(spec["index"], x.reshape(spec["shape"]).astype(np.float32))
        interpreter.invoke()
        return (float(interpreter.get_tensor(stage1).ravel()[0]),
                float(interpreter.get_tensor(out_spec["index"]).ravel()[0]))

    quiet = embeddings()
    q1, q_out = run(quiet)
    if q1 >= 0.5:
        sys.exit(f"referenceEmbeddings() trips the first stage ({q1:.4f}); the quiet case needs another input")

    # Climb from the quiet input, keeping any random step that raises the first stage's score,
    # until it clears 0.5 with room to spare. Seeded, so the fixture is reproducible.
    rng = np.random.default_rng(20260924)
    loud, best = quiet.copy(), q1
    sigma = 0.05
    for i in range(50000):
        cand = loud + rng.normal(0, sigma, loud.shape).astype(np.float32)
        s1, _ = run(cand)
        if s1 > best:
            loud, best = cand, s1
        if best >= 0.75:
            break
    if best < 0.5:
        sys.exit(f"could not find a window that trips the first stage (best {best:.4f})")
    l1, l_out = run(loud)

    with open(out_name, "w") as f:
        f.write(f"# reference tflite output, two-stage classifier of {model}\n")
        f.write("# case <name> <input values following, 0 = referenceEmbeddings()> <first stage> <output>\n")
        f.write(f"case quiet 0 {q1:.8g} {q_out:.8g}\n")
        f.write(f"case loud {loud.size} {l1:.8g} {l_out:.8g}\n")
        for v in loud:
            f.write(f"{v:.8g}\n")
    print(f"wrote {out_name}: quiet first stage {q1:.6f} -> {q_out:.6f}, loud first stage {l1:.6f} -> {l_out:.6f}")


if __name__ == "__main__":
    main()
