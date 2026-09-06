# converge

A collaborative plain-text CRDT for Go, in the YATA family. Replicas that
exchange all of their updates, in any order and with any duplication, end up
holding byte-identical state. Standard library only, no dependencies ever.

    go get github.com/maxxu123456/converge

## Editing

```go
d := converge.NewDoc()
body := d.Text("body")
body.Observe(func(ev converge.Event) { editor.Apply(ev.Delta) })

d.Transact(nil, func(tx *converge.Tx) {
	tx.Insert(body, 0, "hello world")
})
```

Read a Text through the tx inside a transaction. Calling `body.Len()` there
deadlocks, because the callback already holds the document lock. Build with
`-tags converge_debug` and it panics saying so instead of hanging.

## Syncing

`syncproto` frames messages for any duplex byte stream. Peers open with their
own state vector, answer someone else's with whatever that peer is missing,
and apply what arrives.

```go
syncproto.WriteMessage(conn, syncproto.Step1(d.StateVector()))

m, err := syncproto.ReadMessage(bufio.NewReader(conn))
switch m.Type {
case syncproto.TypeStep1:
	sv, _ := converge.ParseStateVector(m.Payload)
	syncproto.WriteMessage(conn, syncproto.Step2(d.EncodeStateAsUpdate(sv)))
case syncproto.TypeStep2, syncproto.TypeUpdate:
	err = d.ApplyUpdate(m.Payload, conn)
}
```

`Text.Position` returns a cursor that survives concurrent edits, and
`awareness` carries presence over the same link.

## Status

Text is the only collaborative type. No maps, no arrays, no rich text, no undo
stack, no persistence, no Yjs wire compatibility. Tombstones are never
collected, so a document only grows, and `Doc.Stats` says when to rotate it.

## License

MIT. See LICENSE.
