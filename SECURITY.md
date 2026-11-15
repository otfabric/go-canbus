# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-canbus`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-canbus/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce (ideally a minimal program or input file), the affected version(s) and the impact you see.

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

## Security Considerations

### CAN has no security

CAN frames carry no sender authentication, no integrity protection beyond
the CRC and no encryption. Any node on a bus can send any identifier. On
vehicles and machines, frames move brakes, drives and power electronics.
This library transmits only what the application asks it to; it never
probes, scans or retransmits on its own. Test against vcan or an isolated
bench bus.

### Untrusted input

DBC/KCD/ARXML files, capture files, slcan byte streams and received frames
are untrusted input. The parsers and decoders are fuzzed (see TESTING.md),
bounded as the table below says, and report what they cannot read as
errors. A first frame announcing more than `MaxMessageSize` is answered with OVERFLOW
and nothing is allocated for it. An ISO-TP sender uses a flow control only
while it waits for one; others are dropped.

Parsing and validating a database takes time and memory that grow with its
size. The parsers hold the whole input and the database built from it: for
input made of the shortest statements or elements that is a few tens of
times the input's size (the element tree of an ARXML document, which
lives until `Parse` returns, is the largest). An application that parses
files it did not write bounds what it reads to what its host affords
(`io.LimitReader`): the fixed bounds below are far above the files of
vehicles and are not a memory budget.

| Input | Bound | Beyond it | Test |
|---|---|---|---|
| DBC source | 64 MiB | `*dbc.ParseError`; nothing is parsed | `dbc` `TestSourceSizeBound`, `TestSourceSizeBoundOfPackage`, `TestTokenMemory` |
| KCD document | 64 MiB | `*kcd.ParseError`; nothing is parsed | `kcd` `TestDocumentSizeBound` |
| ARXML document | 1 GiB | `*arxml.ParseError`; nothing is parsed | `arxml` `TestDocumentSizeBound` |
| Signals of a database (DBC, KCD, ARXML) | 262144 (2^18) | `*ParseError`; the rest of the file is not read | `dbc`, `kcd` `TestSignalBound`; `arxml` `TestDatabaseBounds` |
| Message length (DBC, KCD, ARXML, `candb.Validate`, `codegen.Generate`) | 0..8 bytes, 0..64 for CAN FD | error diagnostic, so a `*ParseError` and an error of `Generate` | `candb` `TestMessageLengthDiagnostics`; `kcd` `TestMessageLengthBound`; `arxml` `TestFrameLengthBound`; `codegen` `TestGenerateMessageLength` |
| Signal start bit and length (`candb`) | 0..2^24; 1..64 bits | `ErrLayout`; error diagnostic | `candb` `TestSignalLayoutBounds` |
| Overlap check of `candb.Database.Validate` | 64 pairs listed per message; 2^24 steps per database | a count of the pairs not listed; one warning where the check ends | `candb` `TestOverlapDiagnosticsBounded`, `TestOverlapCheckBounded`, `TestOverlapWork`; `dbc`, `kcd`, `arxml` `TestSharedBitSignals` |
| ARXML element nesting | 256 levels | `*arxml.ParseError` | `arxml` `TestNestingBound` |
| ARXML value descriptions of a database | 2097152 (2^21) | `*arxml.ParseError`; the rest of the file is not read | `arxml` `TestDatabaseBounds` |
| ARXML TEXTTABLE scale | 256 values, limits finite and within ±2^53 | warning; not expanded | `arxml` `TestTextTableLimits` |
| ARXML diagnostics | 1000, then a count of the others | summary diagnostic | `arxml` `TestDiagnosticsBound` |
| KCD LabelGroup | 256 values a group, 1048576 (2^20) a database | warning; not expanded | `kcd` `TestLabelGroupBounds`, `TestLabelGroupDatabaseBound` |
| Line of a text trace (candump, JSON Lines, ASC, TRC) | 1 MiB | `bufio.ErrTooLong` | `capture` `TestTextLineBound` |
| pcap record | 64 KiB | `*capture.ParseError` | `capture` `TestReadersMemoryBounded` |
| pcapng block | 1 MiB | `*capture.ParseError` | `capture` `TestPCAPNGBlockLimits`, `TestReadersMemoryBounded` |
| pcapng interfaces of a section | 65536 | `*capture.ParseError` | `capture` `TestPCAPNGInterfaceBound` |
| BLF object and container | 16 MiB each | `*capture.ParseError` | `capture` `TestReadersMemoryBounded`, `TestBLFSplitObjectMemory` |
| Trace timestamps | the format's fields and the year 9999 | `*capture.ParseError` | `capture` `TestReaderTimestampRange`, `TestPCAPNGResolutionTable`, `TestPCAPFractionRange`, `TestBLFStartTimeRange` |
| slcan record | 256 bytes | counted (`Stats.RxFailure`) and dropped | `slcan` `TestLongGarbageAndDrops` |
| ISO-TP message | `Config.MaxMessageSize` (default 4095 bytes) | OVERFLOW flow control and `ErrTooLarge`; nothing allocated | `isotp` `TestFirstFrameDoesNotPreallocate`, `TestFlowControlWaitAndOverflow` |
| Queue of a `Bus` subscription | `WithQueueSize` (default 1024 frames) | the overflow policy; counted (`Dropped`) | `canbus` `TestBusOverflowPolicies` |

The bounds are fixed in this release; none of them can be changed by the
caller.

Writers never let a name or string from their input change the structure
of their output: `dbc.Write` rejects names that are not DBC identifiers
(`dbc.ErrName`), and the candump writer refuses interface names it cannot
carry (`capture.ErrUnrepresentable`).

### Privileges

Raw and ISO-TP sockets need no capability. Interface administration
(`socketcan/link` changes) needs `CAP_NET_ADMIN`; opening a socket never
reconfigures an interface.

### Generated code

Generated Go embeds names and comments from the database as identifiers
(sanitized) and comments; it contains no string evaluated at run time.
Comments replace line terminators and control characters with spaces, and
names in error messages are emitted only as quoted Go string literals.
`codegen.Generate` generates only for databases that validate without
errors and whose messages have a length a frame carries (0 to 64 bytes):
the generated `Encode` sizes its payload from that length. Review
generated code from untrusted databases like any other code.
