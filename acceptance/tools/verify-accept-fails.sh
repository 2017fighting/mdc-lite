#!/usr/bin/env bash
#
# Proves the acceptance gate has two sides: it is green when nothing is broken, and
# it is red — for the right reason — when something is. One side alone is not a gate:
# a check that has never been seen failing may simply be a check that cannot fail,
# and a check that fails for anything is noise.
#
#   ./acceptance/tools/verify-accept-fails.sh      (or: make accept-falsify)
#
# Do not pipe this script. Its exit status is its answer, and a pipe would replace
# that answer with the last command's — a mistake that was made twice by hand in the
# repository this one learned from.
#
# What it does, in order:
#
#   1. runs the gate unbroken and requires green **and** the positive marker, because
#      "exit 0" is also what a gate that skipped everything says;
#   2. breaks the part numbering in the product (both discs would get index 1) and
#      requires red **and** the failure message to name the missing second disc;
#   3. restores the source and requires a clean tree.
#
# The gate builds its own binary from the checkout on every run, which is what makes
# step 2 honest: the break is inside the artifact under test, not beside it.
#
# Requirements: Go, Docker with the compose plugin, and python3 (step 2 matches one
# exact snippet of the product's source, so a reformat of that block makes the script
# fail loudly instead of silently breaking nothing).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
# The logs cannot live under the scratch root: the gate resets that root on every run,
# so a log written there would be deleted by the very run that is writing it.
OUT_DIR=$ROOT/acceptance/scratch-falsify
SCRATCH=${MDC_ACCEPT_ROOT:-$ROOT/acceptance/scratch}
GREEN_LOG=$OUT_DIR/green.log
RED_LOG=$OUT_DIR/red.log
TARGET=internal/pipeline/stem.go

mkdir -p "$OUT_DIR"

# The file is snapshotted, not reverted with git: this script runs against a working
# tree that may hold uncommitted work, and a gate check that quietly throws that work
# away is worse than no gate check.
SNAPSHOT=$OUT_DIR/stem.go.orig
cp "$ROOT/$TARGET" "$SNAPSHOT"

restore() {
	cp "$SNAPSHOT" "$ROOT/$TARGET" 2>/dev/null || true
}
trap restore EXIT

step() {
	printf '\n== %s\n' "$*"
}

# 1 — the unbroken gate -------------------------------------------------------------
step "unbroken: the gate must be green, and must say that it ran"
if ! (cd "$ROOT" && make accept) >"$GREEN_LOG" 2>&1; then
	echo "FAIL: the gate is red with nothing broken; read $GREEN_LOG"
	tail -n 20 "$GREEN_LOG"
	exit 1
fi
if ! grep -q 'ACCEPTANCE RAN' "$GREEN_LOG"; then
	echo "FAIL: the gate exited 0 but left no positive marker; a green run that does not say"
	echo "      what it asserted is indistinguishable from a run that asserted nothing."
	exit 1
fi
if [ ! -s "$SCRATCH/ran.json" ]; then
	echo "FAIL: the gate left no marker file at $SCRATCH/ran.json"
	exit 1
fi
echo "green: the gate ran and left its marker"

# 2 — one deliberate break ----------------------------------------------------------
step "broken: both discs numbered 1, so the second disc's basename cannot exist"
python3 - "$ROOT/$TARGET" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()
# The part index of a confident marker (`cd2`) — the one place the number decides the
# output file name. Pinning it to 1 makes both discs want `-cd1`.
old = """		if idx, err := strconv.Atoi(s[loc[2]:loc[3]]); err == nil && idx > 0 {
			return tail{
				stem:  trimSeparators(s[:loc[0]]),
				text:  trimSeparators(s[loc[0]:loc[1]]),
				index: idx,
				tier:  tierConfident,
			}
		}"""
new = old.replace("index: idx,", "index: 1,")
if text.count(old) != 1:
	sys.exit("the break no longer applies: the confident marker block was not found exactly once")
path.write_text(text.replace(old, new))
PY
if cmp -s "$SNAPSHOT" "$ROOT/$TARGET"; then
	echo "FAIL: the break did not change $TARGET, so nothing was actually broken"
	exit 1
fi

step "broken: the gate must be red, and for this reason"
if (cd "$ROOT" && make accept) >"$RED_LOG" 2>&1; then
	echo "FAIL: the gate stayed green with the part numbering broken; read $RED_LOG"
	tail -n 20 "$RED_LOG"
	exit 1
fi
if ! grep -q 'SSIS-001-cd2.mp4 is missing' "$RED_LOG"; then
	echo "FAIL: the gate went red, but not for the break that was made; read $RED_LOG"
	grep -n -- '--- FAIL' "$RED_LOG" || true
	exit 1
fi
if grep -q 'ACCEPTANCE RAN' "$RED_LOG"; then
	echo "FAIL: the red run still printed the positive marker"
	exit 1
fi
echo "red: the gate failed at the assertion that names the missing disc"

# 3 — restore ----------------------------------------------------------------------
step "restored: the source is back to what it was"
restore
if ! cmp -s "$SNAPSHOT" "$ROOT/$TARGET"; then
	echo "FAIL: $TARGET was not put back the way it was found"
	exit 1
fi

printf '\nthe acceptance gate has both sides: green unbroken, red and specific broken\n'
printf 'logs: %s\n' "$OUT_DIR"
