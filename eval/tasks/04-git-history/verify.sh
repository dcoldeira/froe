#!/usr/bin/env bash
grep -qi "witness value rounding" "$1" || exit 1
