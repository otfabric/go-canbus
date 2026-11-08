# Recorded-traffic coverage

What the CAN logs recorded on real vehicles ([testdata/captures](../testdata/captures/README.md))
exercise in go-canbus, and with what result. The machine-readable form is
the `coverage` list of [manifest.json](../testdata/captures/manifest.json).
All sources are evidence class B: the vehicles are the publishers'
statement, not verified independently. Independent decoder: Wireshark 4.6.9
(candump reader, CAN and ISO 15765 dissectors).

## Sources

| Source | Vehicles / devices | Recording | Committed | Extended |
|---|---|---|---:|---:|
| CANdid (Univ. of Adelaide, CC BY 4.0) | 2018 Holden Astra, 2017 Mazda CX-3, 2012 Subaru Impreza ×2, 2012 Holden Barina ×2, 2012 Holden Cruze, 2013 / 2014 Mazda CX-5, 2016 Toyota Yaris | OBD-II port, candump | 5,000 frames | 1,345,579 frames |
| CarDS (TUdatalib, CC BY-SA 4.0) | 2020 electric vehicle (OEM withheld), ten CAN domains; workshop testers T1, T2; CaringCaribou | Vector interfaces, CANoe, candump export with label tokens | 3,996 frames | 12,584,033 frames |
| TU/e intrusion dataset v2 (CC BY-NC 4.0) | Opel Astra, Renault Clio | CANtact (slcan0), candump | none (licence) | 3,076,636 frames |

## Matrix

| Message / behaviour | Recorded in | Decode | Encode / write | Client (tester) | Server (ECU) | Known differences |
|---|---|---|---|---|---|---|
| classical 11-bit frames, DLC 1..8 | all | = Wireshark | byte for byte | n/a | n/a | none |
| 29-bit identifiers | CarDS | = Wireshark | byte for byte | n/a | n/a | none |
| CAN FD, BRS, ESI, lengths 2..64 | CarDS | = Wireshark | byte for byte; pcapng = Wireshark | n/a | n/a | none |
| direction tokens `R`/`T` | CarDS (publisher's labels) | Echo | byte for byte with `SetDirection` (**fixed**: were dropped) | n/a | n/a | pcap, pcapng carry no direction |
| nine interfaces in one log | CarDS | interface names | candump, JSON Lines, pcapng keep names; ASC/BLF/TRC one channel each (**fixed**: all were channel 1) | n/a | n/a | pcap has no names; ASC/BLF/TRC hold numbers |
| out-of-order first line | CANdid (every log) | kept | candump, ASC, TRC exact | n/a | n/a | BLF writes 258 older frames at its start time |
| ISO-TP single frames, padding 00/AA/55/CC | CarDS T1, T2, CaringCaribou | = Wireshark | byte for byte | byte for byte | byte for byte | none |
| ISO-TP multi-frame responses up to 515 bytes, sequence wrap | CarDS T1, T2 | = Wireshark | byte for byte (FF, CF, last-CF padding) | FC byte for byte (BS 0, STmin 1/5 ms) | CF no closer than STmin | recorded ECUs send CFs 8–20 ms apart; go-canbus at STmin |
| first frames never continued | CaringCaribou | reception error per FF | n/a | n/a | n/a | none |
| whole recordings, 17.0 M frames | all three (extended) | decoded | byte for byte (SHA-256 in the manifest) | n/a | n/a | TU/e logs lack a final newline |

## Not in any recording

Remote frames, error frames, DLC 9..15 of classical frames, CAN XL, ISO-TP
extended or mixed addressing, CAN FD ISO-TP, WAIT and OVERFLOW flow
control, multi-frame requests. These remain verified against software
peers only (INTEROPERABILITY.md). No source publishes a DBC, so no
recorded payload is decoded into signals.
