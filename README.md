# converge

A collaborative plain-text CRDT for Go, in the YATA family. Replicas that
exchange all of their updates, in any order and with any duplication, end up
holding byte-identical state.

Standard library only. No dependencies, now or later.

## Install

    go get github.com/maxxu123456/converge

## Status

Early. The package compiles and the identity primitives are in place. The
document type, the wire format and the sync protocol are not written yet, so
there is nothing useful to run.

## License

MIT. See LICENSE.
