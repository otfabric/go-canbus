# Recorded CAN traffic of real vehicles

Excerpts of CAN logs recorded by others on real cars: ten passenger cars at
their OBD-II port (CANdid) and the ten CAN domains of a 2020 electric
vehicle with two workshop diagnostic testers and CaringCaribou on its
diagnostic bus (CarDS). go-canbus's tests decode every line, compare the
result with Wireshark's own reader and dissector, write it back byte for
byte, convert it through every trace format, and run the ISO-TP connection
against the recorded testers and ECUs. This is evidence from real buses,
recorded by others; it is not a test against a live vehicle, not a
conformance claim, and no message is decoded into signals (the publishers
give no DBC).

[manifest.json](manifest.json) is the machine-readable record of all of it:
sources, licences, the original file each excerpt was cut from (URL, size,
SHA-256), the cut, and the counts every excerpt must decode to.
[SHA256SUMS](SHA256SUMS) checks the files (`shasum -a 256 -c SHA256SUMS` in
this directory; the tests check it too). docs/CAPTURE-COVERAGE.md is the
matrix of what the recordings exercise.

## Evidence classes

| Class | Meaning | Here |
|---|---|---|
| A | physical-device traffic, independently verified | none |
| B | physical-device origin documented by the publisher, not independently verified | CANdid, CarDS, TU/e |
| C | recorded traffic of unknown origin | none |
| D | simulated or generated traffic | none |

Nothing here is class A: the vehicles are named by their publishers (CANdid
by make, model and year; CarDS only as a 2020 commercial EV, the OEM
withheld) and their recordings carry no identity a test could check.

## Sources and terms

| Directory | Source | Version | Licence | Committed |
|---|---|---|---|---|
| `candid/` | CANdid, University of Adelaide (Howson, Rohl, Roughan, White, Zanotti), [doi:10.25909/29068553](https://doi.org/10.25909/29068553) | figshare version 2, retrieved 2026-11-08 | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) | first 500 lines of each car's `*_brakes_CAN.log` |
| `cards/` | CarDS, TUdatalib (Hellemans, Hamborg, Lauser, Rabbani, Preneel, Krauß, Mentens), [doi:10.48328/tudatalib-2179](https://doi.org/10.48328/tudatalib-2179) | issued 2026-03-25, retrieved 2026-11-08 | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/), text in [cards/LICENSE.txt](cards/LICENSE.txt) | four excerpts, see below |
| none | Automotive CAN Bus Intrusion Dataset v2, TU Eindhoven (Dupont, Lekidis, den Hartog, Etalle), [doi:10.4121/uuid:b74b4928-c377-4585-9432-2004dfa20a5d](https://doi.org/10.4121/uuid:b74b4928-c377-4585-9432-2004dfa20a5d) | version 2 | CC BY-NC 4.0 | nothing: the non-commercial licence does not fit an MIT repository; extended corpus only |

The excerpts keep their source's licence (the CarDS ones CC BY-SA 4.0); they
are not covered by this repository's MIT licence. Every excerpt is a
deterministic cut of a file the source publishes unchanged, recorded in the
manifest with the original's SHA-256; nothing in the lines was edited.

## Files

| File | Cut from | Cut | Lines | What it holds |
|---|---|---|---:|---|
| `candid/<n>_brakes_CAN.head500.log` | `<n>_brakes_CAN.log` of car n | `head -n 500` | 500 each | classical 11-bit frames of DLC 1..8 from the OBD-II port of car n; the first line of every CANdid log is 12–23 ms newer than the lines after it |
| `cards/Benign-FastDriving-DrivingSchool-NA-NA.head2000.log` | same name, in `CAN.zip` → `CAN/benign.zip` | `head -n 2000` | 2000 | nine CAN domains (`can0`..`can8`) while driving: CAN FD with BRS (1432 frames) and ESI (7), 29-bit identifiers (222), CAN FD lengths 12..64, gateway copies of a frame on several domains |
| `cards/Benign-Standstill-University-NA-T2_ReadVIN.can9.log` | same name without `.can9` | `grep ' can9 '` | 96 | diagnostic tester T2 on the diagnostic domain: UDS session, tester present and 24 identification reads of one ECU (29-bit IDs 0x17FC0087 → 0x17FE0087), ISO-TP multi-frame responses with flow control BS 0, STmin 5 ms |
| `cards/Benign-Standstill-University-NA-T1_RecognizeCar.can9.log` | same name without `.can9` | `grep ' can9 '` | 299 | tester T1 recognising the car: sessions and identification reads of 20 ECUs (11- and 29-bit), negative responses, a 515-byte response in 83 consecutive frames (sequence numbers wrap), flow control BS 0, STmin 1 ms, padding bytes 00, AA, 55 and CC |
| `cards/UDS-Standstill-University-NA-CaringCaribou_Auto_1.can9.3700-5300.log` | `UDS-Standstill-University-NA-CaringCaribou_Auto_1.log` in `CAN/uds.zip` | `grep ' can9 ' \| sed -n 3700,5300p` | 1601 | CaringCaribou's UDS enumeration (attack class G): single frames, negative responses, 180 first frames that no consecutive frame follows (the recording holds no consecutive frame) |
| `cards/*.isotp.json` | the `.can9` logs | `tshark -r <log> -d can.subdissector=iso15765`, Wireshark 4.6.9 | | Wireshark's ISO 15765 dissection: every single frame and reassembled message, every flow control and first frame |
| `cards/LICENSE.txt` | the dataset's `LICENSE.txt` | unchanged | | CC BY-SA 4.0 |

The CarDS logs are in candump format with a direction token after every
frame. The publisher uses the token as the label: `T` marks a frame of an
attack, `R` everything else. It is not the logging node's direction.

## How the tests use them

| Test | Checks |
|---|---|
| `internal/corpus` `TestCommittedCorpusVerifies` | SHA256SUMS and the manifest list every file with its hash |
| `capture` `TestVehicleCapturesDecode` | every line decodes, to the counts in the manifest (frames, standard, extended, FD, BRS, ESI, remote, error, T marks, tokens, out-of-order times, interfaces) |
| `capture` `TestWiresharkReadsVehicleCaptures` | Wireshark's candump reader and CAN dissector decode every line to the same frame and interface; Wireshark dissects the pcapng go-canbus writes of them the same way (tshark 4.4 or later; required in CI) |
| `capture` `TestVehicleCapturesCandumpRoundTrip` | read and written back, every excerpt is byte for byte the original (with `CandumpWriter.SetDirection` for the CarDS logs) |
| `capture` `TestVehicleCapturesFormats` | JSON Lines, pcap, pcapng, ASC, BLF and TRC carry every recorded frame; each recorded interface keeps a channel of its own in ASC, BLF and TRC |
| `capture` `TestVehicleCapturesBLFStart`, `TestVehicleCapturesReplay` | the out-of-order first line of the CANdid logs: BLF writes the older frames at its start time; replay sends every frame once in file order |
| `capture` `TestVehicleLinesMalformed`, `FuzzReaders` seeds | truncated, stretched and corrupted recorded lines; real lines seed the reader fuzzer |
| `isotp` `TestCaptureReassemblyMatchesWireshark` | a listen-only connection per identifier reassembles exactly Wireshark's messages; each first frame the recording leaves incomplete ends in a reception error |
| `isotp` `TestCaptureTesterSide` | a connection set up as each recorded tester (padding byte, BS, STmin) sends its requests and flow controls byte for byte and receives Wireshark's responses (21 tester–ECU pairs) |
| `isotp` `TestCaptureECUSide` | a connection set up as each recorded ECU (padding byte) receives the requests and sends its single, first and consecutive frames byte for byte, no closer than STmin |
| `capture` `TestExtendedVehicleCorpus` | with the extended corpus: each whole recording has its SHA-256, decodes to its frame count and is written back byte for byte |

## Extended corpus

`make captures-extended` downloads the whole recordings listed under
`extended` in the manifest (about 1.2 GB to download, 1.0 GB on disk: ten CANdid brake logs, four
CarDS logs, the TU/e Opel Astra and Renault Clio captures) into
`.cache/captures` (or `$CANBUS_CAPTURES`), checks their SHA-256 and runs
`TestExtendedVehicleCorpus` on them (17.0 million frames). The normal tests
never need them. CarDS's `CAN.zip` is 3.6 GB; `internal/cmd/fetchcaptures`
reads its directory with HTTP range requests and fetches only `uds.zip` and
`benign.zip`.

## Adding a recording

Check its licence first: commit an excerpt only if the licence allows
redistribution (and keep its licence with it); otherwise list it under
`extended` only. Cut it deterministically, add the file to SHA256SUMS and
manifest.json (original URL, size, SHA-256, the cut, the class and the
counts it decodes to; cross-check the counts with tshark), and add a line to
the tables above.
