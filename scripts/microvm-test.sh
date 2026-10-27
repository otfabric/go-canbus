#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# microvm-test.sh: run the SocketCAN, netlink and kernel ISO-TP tests in a
# microVM booted with a chosen kernel (virtme-ng on QEMU/KVM), instead of
# the host kernel. The guest shares the host's file system read-only, so
# test binaries built on the host run inside unchanged.
#
#   scripts/microvm-test.sh <vmlinuz> <root containing lib/modules/<release>>
set -euo pipefail
kernel=$1 moddir=$2
out=$(mktemp -d)
GOWORK=off go test -c -o "$out/socketcan.test" ./socketcan
GOWORK=off go test -c -o "$out/link.test" ./socketcan/link
cat > "$out/guest.sh" <<GUEST
set -e
uname -a
modprobe -a can can-raw vcan vxcan can-isotp can-gw
ip link add vcan0 type vcan && ip link set vcan0 mtu 72 up
cd $PWD/socketcan && CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_ISOTP=1 $out/socketcan.test -test.count=1
cd $PWD/socketcan/link && CANBUS_REQUIRE_VCAN=1 CANBUS_TEST_ADMIN=1 $out/link.test -test.count=1
echo MICROVM-OK
GUEST
start=$(date +%s)
vng --run "$kernel" --root / --user root --memory 2G --cpus 2 \
    --rwdir "$out" \
    --exec "MODPROBE_OPTIONS='-d $moddir' bash $out/guest.sh" | tee "$out/log"
echo "microVM run took $(( $(date +%s) - start )) s"
grep -q MICROVM-OK "$out/log"
