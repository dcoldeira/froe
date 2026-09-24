import unittest

from qrl.causal import causal_witness, witness_value


class WitnessTest(unittest.TestCase):
    def test_identity_is_not_certified(self):
        omega = causal_witness()
        identity = [1.0 / 16] * 16
        self.assertGreaterEqual(witness_value(omega, identity), 0)


if __name__ == "__main__":
    unittest.main()
