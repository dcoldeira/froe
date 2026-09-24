"""Generalised robustness against white noise."""

from .witness import causal_witness, witness_value


def robustness(process, steps=1000):
    """Smallest noise weight r at which the witness stops certifying."""
    omega = causal_witness()
    noise = [1.0 / len(process)] * len(process)
    for i in range(steps + 1):
        r = i / steps
        mixed = [(p + r * n) / (1 + r) for p, n in zip(process, noise)]
        if witness_value(omega, mixed) >= 0:
            return r
    return None
