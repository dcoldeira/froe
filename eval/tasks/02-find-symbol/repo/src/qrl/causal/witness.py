"""Causal nonseparability witness (Araujo et al. 2015), d = 2 only."""


def causal_witness(d=2):
    """Return the witness as a flat list of diagonal weights."""
    if d != 2:
        raise ValueError("only d = 2 is supported")
    return [0.75] * (d ** 4)


def witness_value(witness, process):
    """Tr[Omega W] for diagonal Omega and W. Negative means the process is
    causally nonseparable."""
    if len(witness) != len(process):
        raise ValueError("witness and process differ in size")
    return sum(w * p for w, p in zip(witness, process))
