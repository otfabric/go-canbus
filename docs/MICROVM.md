# MicroVMs for kernel and layer-2 testing: evaluation

Question: can OT Fabric use microVMs where tests depend on the kernel or on
layer 2 (CAN, raw Ethernet), instead of whatever kernel the CI runner or the
developer's Docker happens to have?

**Answer: yes, and go-canbus now does.** The `Kernels` workflow boots
virtme-ng microVMs on GitHub's amd64 runners with five kernels and runs the
SocketCAN, netlink and kernel ISO-TP tests in each. It found a real issue in
its first run.

## Why it matters

| Problem seen in this project | What a microVM changes |
|------------------------------|------------------------|
| GitHub's amd64 runner kernel (linux-azure) has no `can-isotp`; we build it out of tree from kernel source | Boot a kernel that has it: Ubuntu generic or mainline |
| The runner kernel changes under us (6.17 → 7.0 between jobs today) | Pin the kernel per test job |
| Behaviour differs between kernels (vcan's default MTU went from 72 to 2060 with CAN XL in 6.2; ISO-TP fixes land per release) | Test a matrix: LTS kernels users run, plus the newest |
| Docker Desktop's kernel on macOS has no CAN modules and cannot load any | A local VM with a real kernel (Lima today) |
| Admin tests (create/delete interfaces, up/down) need root on the host | Root inside a throw-away guest |

## Measured (2026-10-27, GitHub `ubuntu-24.04`, amd64, KVM)

| Kernel | Result | VM wall time (boot + tests) |
|--------|--------|------------------------------|
| Ubuntu 24.04 generic 6.8.0-147 | pass after fix (failed before: see below) | 42 s |
| mainline 6.1 | pass | 35 s |
| mainline 6.6 | pass | 26 s |
| mainline 6.8 | pass | 28 s |
| mainline 6.12 | pass | 27–37 s |
| mainline 6.18 | pass | 21 s |
| mainline 5.10 | virtme-ng 1.42 exits without output: too old for its guest init | — |

Per job, about 1 minute goes to installing QEMU and virtme-ng and
downloading the kernel packages (~230 MB for Ubuntu's, less for mainline).

**Finding.** Under Ubuntu's 6.8 kernel in a 2-vCPU guest, the kernel ISO-TP
sender bursting at STmin 0 overflowed the receive queue of the userspace
ISO-TP receiver's socket: the kernel reported 16 dropped frames, and the
receiver correctly failed with a sequence error. Mainline 6.8 on the same
runner passed, so the exact kernel build and timing matter. A userspace
receiver that advertises STmin 0 must absorb a block at bus speed: give its
socket a large buffer (`socketcan.WithReceiveBuffer(1 << 20)`) or advertise a
non-zero STmin. The tests do the first, and LIMITATIONS.md says so. Neither
the vcan job nor the Lima VM had shown it.

## Options compared

| Tool | Boots | Kernel choice | File system | Fits for |
|------|-------|---------------|-------------|----------|
| **virtme-ng** (QEMU microvm/KVM) | 1–3 s | any built kernel + its modules; Ubuntu and mainline debs work unchanged | host root shared read-only (virtiofs/9p): test binaries built on the host run as-is | **CI kernel matrices; chosen** |
| QEMU microvm + own initramfs | 1–3 s | any, also very old (5.10) | build an initramfs | Old kernels virtme-ng cannot boot |
| Firecracker | < 1 s | custom minimal kernels (no modules by default) | block-device rootfs image | Many isolated guests at scale; too much image plumbing for test jobs |
| Cloud Hypervisor | ~1 s | custom kernels, virtiofs | virtiofs | Similar to Firecracker, more devices; no gain over virtme-ng here |
| Kata Containers | 1–2 s | Kata's guest kernel | container image | VM-isolated containers in Kubernetes; the guest kernel is Kata's, not ours |
| gVisor | fast | none: user-space kernel | container | Unsuitable: no AF_CAN, no AF_PACKET raw L2 |
| Lima (vz) / vfkit / krunvm on macOS | 5–20 s | distribution kernel | shared home | Local development on Macs (Lima is what this project used) |
| Docker Desktop | — | its LinuxKit kernel only, no CAN modules | — | Unsuitable for CAN |

Constraints found: GitHub's **arm64** runners have **no `/dev/kvm`**, so
microVMs there would fall back to QEMU emulation (TCG), roughly 10–20×
slower: usable nightly, not per push. Apple M1/M2 Macs have no nested
virtualization, so microVMs cannot run inside Lima there; run them directly
on macOS (Lima, vfkit) or on Linux machines.

## Layer 2 beyond CAN

The same approach applies to the Ethernet-level stacks:

- **PROFINET** (DCP, RT frames over raw Ethernet, EtherType 0x8892),
  **EtherNet/IP** (multicast I/O) and **LLDP**: a guest gives a private
  kernel with `veth` pairs, bridges, `tc`/`netem` impairments, VLANs and
  AF_PACKET, with root, without touching the runner's network.
- For plain topology isolation, **network namespaces** on the runner
  (`ip netns`, `veth`, `vxcan` for CAN) are cheaper and enough. Use a microVM
  when the **kernel** matters: a specific version, a module the runner lacks
  (`can-isotp`, `can-gw`, `sch_taprio`/`sch_etf` for TSN, `macsec`), AF_PACKET
  or timestamping differences, or tests that must load modules.
- The same microVM can run the interop containers if Docker runs inside
  it; with virtme-ng that needs a writable root (`--rw` overlay). For CAN
  this is not needed: the containers share the host's network namespace and
  only the kernel's CAN modules matter.

## Recommendation

1. Keep the go-canbus `Kernels` workflow (amd64, KVM): Ubuntu generic plus
   mainline 6.1, 6.6, 6.12 and the current release, on every push.
   `make microvm KERNEL=v6.12` runs the same locally on a Linux host with
   KVM.
2. Use the same script pattern in canbusctl (its vcan tests) and
   canbus-interop (kernel ISO-TP via can-utils) instead of building
   `can-isotp` out of tree; drop the out-of-tree build once both use it.
3. For PROFINET and EtherNet/IP, add a microVM job for kernel-sensitive L2
   behaviour (AF_PACKET, multicast, VLAN tags, `taprio`); keep plain
   namespaces for topology tests.
4. Kernels older than 5.15: QEMU with an initramfs, only if a user needs
   them; ISO-TP is in mainline only since 5.10.
5. arm64: run the matrix on amd64 per push and, if needed, one arm64 TCG job
   nightly.
