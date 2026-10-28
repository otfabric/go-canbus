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
GOWORK=off go test -c -o "$out/slcan.test" ./slcan
GOWORK=off go test -c -o "$out/chaos.test" ./chaos
GOWORK=off go test -c -o "$out/e2e.test" ./e2e
cat > "$out/guest.sh" <<GUEST
set -e
uname -a
modprobe -a can can-raw vcan vxcan can-isotp can-gw slcan
# Let tests have the socket receive buffers they ask for (1 MiB for a
# userspace ISO-TP receiver at STmin 0).
sysctl -qw net.core.rmem_max=4194304
for i in vcan0 vcan1; do ip link add \$i type vcan && ip link set \$i mtu 72 up; done
# CAN XL exists from Linux 6.2 on: required there, absent before.
xl=0
if [ "\$(printf '%s\n6.2\n' "\$(uname -r | cut -d- -f1)" | sort -V | head -1)" = 6.2 ]; then
  ip link add vcanxl0 type vcan && ip link set vcanxl0 mtu 2060 up && xl=1
fi
cd $PWD/socketcan && CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_ISOTP=1 CANBUS_REQUIRE_XL=\$xl $out/socketcan.test -test.count=1
cd $PWD/socketcan && $out/socketcan.test -test.count=1 -test.v -test.run XL | grep -E '^(--- |ok|PASS|FAIL)' 
cd $PWD/socketcan/link && CANBUS_REQUIRE_VCAN=1 CANBUS_TEST_ADMIN=1 $out/link.test -test.count=1
cd $PWD/slcan && CANBUS_TEST_ADMIN=1 CANBUS_REQUIRE_SLCAN=1 $out/slcan.test -test.count=1
cd $PWD/chaos && CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_ISOTP=1 $out/chaos.test -test.count=1 -test.run MonkeyKernel
cd $PWD/e2e && CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_ISOTP=1 $out/e2e.test -test.count=1 -test.short -test.run "VehicleVCAN"
echo MICROVM-OK
GUEST
start=$(date +%s)
vng --run "$kernel" --root / --user root --memory 2G --cpus 2 \
    --rwdir "$out" \
    --exec "MODPROBE_OPTIONS='-d $moddir' bash $out/guest.sh" | tee "$out/log"
echo "microVM run took $(( $(date +%s) - start )) s"
grep -q MICROVM-OK "$out/log"
