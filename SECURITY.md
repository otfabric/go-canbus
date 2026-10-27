# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-canbus`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-canbus/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce (ideally a minimal program or input file), the affected version(s) and the impact you see.

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x | Yes |

## Security Considerations

### CAN has no security

CAN frames carry no sender authentication, no integrity protection beyond
the CRC and no encryption. Any node on a bus can send any identifier. On
vehicles and machines, frames move brakes, drives and power electronics.
This library transmits only what the application asks it to; it never
probes, scans or retransmits on its own. Test against vcan or an isolated
bench bus.

### Untrusted input

DBC/KCD files, capture files and received frames are untrusted input. The
parsers and decoders are fuzzed (see TESTING.md), bounded (line length
1 MiB for text traces, 64 KiB pcap records, 1 MiB pcapng blocks, ISO-TP
`MaxMessageSize`, default 4095 bytes) and report errors instead of
panicking. A first frame announcing more than `MaxMessageSize` is answered
with OVERFLOW and nothing is allocated for it.

### Privileges

Raw and ISO-TP sockets need no capability. Interface administration
(`socketcan/link` changes) needs `CAP_NET_ADMIN`; opening a socket never
reconfigures an interface.

### Generated code

Generated Go embeds names and comments from the database as identifiers
(sanitized) and comments; it contains no string evaluated at run time.
Review generated code from untrusted databases like any other code.
