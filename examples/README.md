# Examples

| Example | Needs | Shows |
|---------|-------|-------|
| [monitor](monitor) | Linux, a CAN interface | Passive monitoring with an optional kernel filter |
| [send](send) | Linux | Sending a classical frame given in cansend notation |
| [send-fd](send-fd) | Linux, CAN FD MTU | Sending a 64-byte CAN FD frame with BRS |
| [dbc-decode](dbc-decode) | nothing | Parsing a DBC, decoding and encoding signals |
| [dbc-generate](dbc-generate) | nothing | Generating typed Go and using generated code |
| [isotp-exchange](isotp-exchange) | nothing | A complete ISO-TP transfer with flow control |
| [virtual-bus](virtual-bus) | nothing | Simulation with injected faults and loss detection |
| [capture-replay](capture-replay) | nothing | Recording to candump format and replaying at 2× |
| [vcan-pingpong](vcan-pingpong) | Linux, vcan | Two processes talking over vcan |

`make examples` runs the ones that need nothing (in CI on every push).
