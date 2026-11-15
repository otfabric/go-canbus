#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# fuzz.sh: run every Fuzz target of the packages given, for FUZZTIME each
# (default 30s). The targets are found with `go test -list '^Fuzz'`: a new
# target runs without a change here or in the Makefile.
#
# Go before 1.28 can end a clean -fuzztime run with "--- FAIL ...
# context deadline exceeded" although no input failed
# (https://github.com/golang/go/issues/75804). A run that fails with that
# message, wrote no failing input and did not fail on a seed is reported and
# treated as a pass; any other failure fails the run.
set -uo pipefail

go_cmd=${GO:-go}
fuzztime=${FUZZTIME:-30s}
# Overwritten for each target.
log="${TMPDIR:-/tmp}/go-canbus-fuzz.log"

found=0
for pkg in "$@"; do
	if ! list=$("$go_cmd" test -list '^Fuzz' "$pkg" 2>&1); then
		echo "$list" >&2
		exit 1
	fi
	for t in $(printf '%s\n' "$list" | grep '^Fuzz'); do
		found=$((found + 1))
		echo "fuzz $pkg $t"
		"$go_cmd" test -run '^$' -fuzz "^$t\$" -fuzztime "$fuzztime" "$pkg" 2>&1 | tee "$log"
		status=${PIPESTATUS[0]}
		[ "$status" -eq 0 ] && continue
		if grep -q 'context deadline exceeded' "$log" &&
			! grep -q -e 'Failing input written to' -e 'failure while testing seed corpus entry' "$log"; then
			echo "fuzz $pkg $t: spurious deadline failure at the -fuzztime end (golang/go#75804); no failing input, treated as a pass"
			continue
		fi
		exit "$status"
	done
done
if [ "$found" -eq 0 ]; then
	echo "no Fuzz targets found" >&2
	exit 1
fi
echo "fuzz: $found targets run for $fuzztime each"
