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
GOWORK=off go test -c -o "$out/chaos.test" ./chaos
GOWORK=off go test -c -o "$out/e2e.test" ./e2e
cat > "$out/guest.sh" <<GUEST
set -e
uname -a
modprobe -a can can-raw vcan vxcan can-isotp can-gw
for i in vcan0 vcan1; do ip link add \$i type vcan && ip link set \$i mtu 72 up; done
cd $PWD/socketcan && CANBUS_REQUIRE_VCAN=1 CANBUS_REQUIRE_ISOTP=1 $out/socketcan.test -test.count=1
cd $PWD/socketcan/link && CANBUS_REQUIRE_VCAN=1 CANBUS_TEST_ADMIN=1 $out/link.test -test.count=1
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
