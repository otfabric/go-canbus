#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# mainline-kernel.sh <version> <dir>: download a mainline kernel build of
# kernel.ubuntu.com (e.g. v6.6) for amd64 and unpack its image and modules
# into <dir>. Prints the kernel release.
set -euo pipefail
v=$1 dir=$2
base="https://kernel.ubuntu.com/mainline/$v/amd64/"
mkdir -p "$dir"
index=$(curl -fsSL "$base")
for pat in 'linux-image-unsigned-[^"]*-generic_[^"]*_amd64\.deb' 'linux-modules-[^"]*-generic_[^"]*_amd64\.deb'; do
    f=$(grep -oE "href=\"$pat\"" <<<"$index" | head -1 | sed 's/href="//; s/"$//')
    [ -n "$f" ] || { echo "no $pat for $v" >&2; exit 1; }
    curl -fsSL "$base$f" -o "$dir/$f"
    dpkg-deb -x "$dir/$f" "$dir/root"
done
ls "$dir/root/lib/modules"
