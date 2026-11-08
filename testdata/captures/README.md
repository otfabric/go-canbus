# Recorded CAN traffic of real vehicles

CAN logs recorded by others on real vehicles, byte for byte as their
publishers distribute them: ten passenger cars at their OBD-II port
(CANdid), the ten CAN domains of a 2020 electric vehicle with two workshop
testers and CaringCaribou on its diagnostic bus (CarDS), and a Renault Clio
(TU Eindhoven). 2.7 million frames, 131 MB. go-canbus's tests decode every
line, compare the result with Wireshark's own reader and dissector, write
it back byte for byte, convert it through every trace format, and run the
ISO-TP connection against the recorded testers and ECUs. This is evidence
from real buses, recorded by others; it is not a test against a live
vehicle, not a conformance claim, and no message is decoded into signals
(the publishers give no DBC).

[manifest.json](manifest.json) is the machine-readable record of all of it:
sources, licences, each file's original name, URL, size and SHA-256, the cut
where a file is one, the counts every log must decode to, and a coverage
list. [SHA256SUMS](SHA256SUMS) checks the files (`shasum -a 256 -c
SHA256SUMS` in this directory; the tests check it too).
[docs/CAPTURE-COVERAGE.md](../../docs/CAPTURE-COVERAGE.md) is the matrix of
what the recordings exercise.

## Evidence classes

| Class | Meaning | Here |
|---|---|---|
| A | physical-device traffic, independently verified | none |
| B | physical-device origin documented by the publisher, not independently verified | CANdid, CarDS, TU/e |
| C | recorded traffic of unknown origin | none |
| D | simulated or generated traffic | none |

Nothing here is class A: the vehicles are named by their publishers (CANdid
by make, model and year in its `*_meta.json` files, committed next to each
log; TU/e by make and model; CarDS only as a 2020 commercial EV) and the
recordings carry no identity a test could check.

## Sources

| Directory | Source | Version | Licence |
|---|---|---|---|
| `candid/` | CANdid, University of Adelaide (Howson, Rohl, Roughan, White, Zanotti), [doi:10.25909/29068553](https://doi.org/10.25909/29068553) | figshare version 2, retrieved 2026-11-08 | CC BY 4.0 |
| `cards/` | CarDS, TUdatalib (Hellemans, Hamborg, Lauser, Rabbani, Preneel, Krauß, Mentens), [doi:10.48328/tudatalib-2179](https://doi.org/10.48328/tudatalib-2179) | issued 2026-03-25, retrieved 2026-11-08 | CC BY-SA 4.0 ([cards/LICENSE.txt](cards/LICENSE.txt)) |
| `tue/` | Automotive CAN Bus Intrusion Dataset v2, TU Eindhoven (Dupont, Lekidis, den Hartog, Etalle), [doi:10.4121/uuid:b74b4928-c377-4585-9432-2004dfa20a5d](https://doi.org/10.4121/uuid:b74b4928-c377-4585-9432-2004dfa20a5d) | version 2, retrieved 2026-11-08 | CC BY-NC 4.0 |

## Names

`<tool-or-tester>_<vehicle>_<what-it-holds>.<ext>`, lower case, words
joined by hyphens; the directory is the source and the extension keeps the
original format. Most files are the whole recording; where a file is a cut (a line range)
the cut is in the manifest and the table below, not in the name. A golden file has the
stem of the log it was made from.

| Tool or tester | Meaning |
|---|---|
| `obd-logger` | CANdid's candump logger at the OBD-II port |
| `vector-canoe` | CarDS's Vector interfaces and CANoe, all CAN domains |
| `tester-t1`, `tester-t2` | CarDS's recordings of its two workshop diagnostic testers (the publisher's T1 and T2) at work: all ten domains, the testers on `can9` |
| `caringcaribou` | CaringCaribou's UDS enumeration, the lines of the diagnostic domain `can9` |
| `cantact` | TU/e's CANtact at the OBD-II port, candump |

Vehicles: CANdid's make, model and year (`-car3`, `-car6` tell its two
2012 Subaru Imprezas apart, `-automatic`, `-manual` its two Holden
Barinas); `2020-ev` is the CarDS vehicle, whose make the publisher withholds;
TU/e gives make and model only.

## Files

| File | Original name | Cut | Frames | What it holds |
|---|---|---|---:|---|
| `candid/obd-logger_2018-holden-astra_brakes.log` | `1_brakes_CAN.log` | whole | 193,889 | 81 s of the OBD-II port while the brake pedal is pressed and released every 10 s: classical 11-bit frames, DLC 1..8; the first line of every CANdid log is 12–23 ms newer than the lines after it |
| `candid/obd-logger_2017-mazda-cx-3_brakes.log` | `2_brakes_CAN.log` | whole | 184,634 | as above |
| `candid/obd-logger_2012-subaru-impreza-car3_brakes.log` | `3_brakes_CAN.log` | whole | 92,009 | as above |
| `candid/obd-logger_2012-holden-barina-automatic_brakes.log` | `4_brakes_CAN.log` | whole | 186,339 | as above |
| `candid/obd-logger_2012-holden-cruze_brakes.log` | `5_brakes_CAN.log` | whole | 198,565 | as above |
| `candid/obd-logger_2012-subaru-impreza-car6_brakes.log` | `6_brakes_CAN.log` | whole | 91,699 | as above |
| `candid/obd-logger_2013-mazda-cx-5_brakes.log` | `7_brakes_CAN.log` | whole | 25,085 | as above |
| `candid/obd-logger_2014-mazda-cx-5_brakes.log` | `8_brakes_CAN.log` | whole | 140,670 | as above |
| `candid/obd-logger_2016-toyota-yaris_brakes.log` | `9_brakes_CAN.log` | whole | 70,096 | as above |
| `candid/obd-logger_2012-holden-barina-manual_brakes.log` | `10_brakes_CAN.log` | whole | 162,593 | as above |
| `candid/<stem>.meta.json` | `<n>_brakes_meta.json` | whole | | the publisher's description of the car and the capture |
| `cards/tester-t2_2020-ev_read-vin.log` | `Benign-Standstill-University-NA-T2_ReadVIN.log` (`CAN.zip` → `CAN/benign.zip`) | whole | 243,479 | all ten domains (CAN FD with BRS and ESI, 29-bit identifiers) while tester T2 reads identification data on `can9`: UDS session, tester present, 24 reads of one ECU (0x17FC0087 → 0x17FE0087), ISO-TP multi-frame responses with flow control BS 0, STmin 5 ms |
| `cards/tester-t1_2020-ev_recognize-car.log` | `Benign-Standstill-University-NA-T1_RecognizeCar.log` (`CAN/benign.zip`) | whole | 282,980 | all ten domains while tester T1 recognises the car on `can9`: sessions and identification reads of 20 ECUs (11- and 29-bit), negative responses, a 515-byte response in 83 consecutive frames (sequence numbers wrap), flow control BS 0, STmin 1 ms, padding bytes 00, AA, 55 and CC |
| `cards/vector-canoe_2020-ev_bus-silence-wake-up.log` | `Benign-Standstill-University-NA-BusSilenceWakeUp.log` (`CAN/benign.zip`) | whole | 436,047 | nine domains at standstill while the buses fall silent and wake up |
| `cards/vector-canoe_2020-ev_fast-driving.log` | `Benign-FastDriving-DrivingSchool-NA-NA.log` (`CAN/benign.zip`, 144 MB) | `head -n 2000` | 2,000 | nine domains while driving: CAN FD with BRS and ESI, 29-bit identifiers, CAN FD lengths 12..64, gateway copies of a frame on several domains |
| `cards/caringcaribou_2020-ev_uds-scan.log` | `UDS-Standstill-University-NA-CaringCaribou_Auto_1.log` (`CAN/uds.zip`, 626 MB) | `grep ' can9 ' \| sed -n 3700,5300p` | 1,601 | CaringCaribou's UDS enumeration (attack class G): single frames, negative responses, 180 first frames that no consecutive frame follows |
| `cards/<stem>.isotp.json` | made here | the log's `can9` lines through `tshark -d can.subdissector=iso15765`, Wireshark 4.6.9 | | Wireshark's ISO 15765 dissection: every single frame and reassembled message, every flow control and first frame, numbered by line of the whole log |
| `cards/LICENSE.txt` | `LICENSE.txt` | whole | | CC BY-SA 4.0 |
| `tue/cantact_renault-clio_driving.log` | `RenaultClio/full_data_capture.log` (`data.zip`) | whole | 386,567 | 275 s of urban driving at the OBD-II port: classical 11-bit frames, DLC 1..8; the file ends without a newline |
| `tue/README` | `RenaultClio/README` | whole | | the publisher's description |

The CarDS logs are in candump format with a direction token after every
frame. The publisher uses the token as the label: `T` marks a frame of an
attack, `R` everything else. It is not the logging node's direction.

## How the tests use them

| Test | Checks |
|---|---|
| `internal/corpus` `TestCommittedCorpusVerifies` | SHA256SUMS and the manifest list every file with its hash |
| `capture` `TestVehicleCapturesDecode` | every line decodes, to the counts in the manifest (frames, standard, extended, FD, BRS, ESI, remote, error, T marks, tokens, out-of-order times, interfaces) |
| `capture` `TestWiresharkReadsVehicleCaptures` | Wireshark's candump reader and CAN dissector decode every line to the same frame and interface; Wireshark dissects the pcapng go-canbus writes of them the same way (tshark 4.4 or later; required in CI) |
| `capture` `TestVehicleCapturesCandumpRoundTrip` | read and written back, every log is byte for byte the original (with `CandumpWriter.SetDirection` for the CarDS logs) |
| `capture` `TestVehicleCapturesFormats` | JSON Lines, pcap, pcapng, ASC, BLF and TRC carry every recorded frame; each recorded interface keeps a channel of its own in ASC, BLF and TRC |
| `capture` `TestVehicleCapturesBLFStart`, `TestVehicleCapturesReplay` | the out-of-order first line of the CANdid logs: BLF writes the older frames at its start time; replay sends every frame once in file order |
| `capture` `TestVehicleLinesMalformed`, `FuzzReaders` seeds | truncated, stretched and corrupted recorded lines; real lines seed the reader fuzzer |
| `isotp` `TestCaptureReassemblyMatchesWireshark` | a listen-only connection per identifier reassembles exactly Wireshark's messages; each first frame the recording leaves incomplete ends in a reception error |
| `isotp` `TestCaptureTesterSide` | a connection set up as each recorded tester (padding byte, BS, STmin) sends its requests and flow controls byte for byte and receives Wireshark's responses (21 tester–ECU pairs) |
| `isotp` `TestCaptureECUSide` | a connection set up as each recorded ECU (padding byte) receives the requests and sends its single, first and consecutive frames byte for byte, no closer than STmin |
| `capture` `TestExtendedVehicleCorpus` | with the extended corpus: each whole recording has its SHA-256, decodes to its frame count and is written back byte for byte |

`make test-captures` runs these tests on the whole logs (CI's vcan jobs,
with Wireshark required); a `-race` build checks the first 20,000 lines of
each log.

## Extended corpus

Three recordings are too large to commit: CarDS's whole fast-driving log
(144 MB) and CaringCaribou log (626 MB), and TU/e's Opel Astra capture
(124 MB). `make captures-extended` downloads them (about 1.1 GB: CarDS's
`CAN.zip` is 3.6 GB, and `internal/cmd/fetchcaptures` reads its directory
with HTTP range requests and fetches only `uds.zip` and `benign.zip`) into
`.cache/captures` (or `$CANBUS_CAPTURES`) under the same clean names
(`tue/cantact_opel-astra_driving.log`), checks their SHA-256 and runs
`TestExtendedVehicleCorpus` on them: 14.7 million more frames, each
recording decoded and written back byte for byte. The normal tests never
need them.

## Adding a recording

Commit the file byte for byte when it is under about 50 MB, otherwise list
it under `extended` (or commit a deterministic cut of it). Name it by the
scheme above, add it to SHA256SUMS and
manifest.json (original name, URL, size, SHA-256, the cut, the class and the
counts it decodes to; cross-check the counts with tshark), and add a line to
the tables above.
