#!/usr/bin/env bash
grep -qi "borehole depth rounding" "$1" || exit 1
