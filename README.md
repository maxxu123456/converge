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
	tx.Insert(body, 0, "hello")
	tx.Insert(body, tx.Len(body), " world")
})
```

Read a Text through the tx inside a transaction. Calling `body.Len()` there
deadlocks, because the callback already holds the document lock.

## Syncing

`syncproto` frames messages for any duplex byte stream. Both peers open with
their own state vector and then run the same loop. Local edits go out from an
`OnUpdate` observer as a TypeUpdate message.

```go
r := bufio.NewReader(conn) // one for the life of the connection
err := syncproto.WriteMessage(conn, syncproto.Step1(d.StateVector()))
for err == nil {
	var m syncproto.Message
	if m, err = syncproto.ReadMessage(r); err != nil {
		break
	}
	switch m.Type {
	case syncproto.TypeStep1:
		var sv converge.StateVector
		if sv, err = converge.ParseStateVector(m.Payload); err == nil {
			u := d.EncodeStateAsUpdate(sv)
			err = syncproto.WriteMessage(conn, syncproto.Step2(u))
		}
	case syncproto.TypeStep2, syncproto.TypeUpdate:
		err = d.ApplyUpdate(m.Payload, conn)
	}
}
return err
```

## Status

Text is the only collaborative type. No maps, no arrays, no rich text, no undo
stack, no persistence, no Yjs wire compatibility. Tombstones are never
collected, so a document only grows, and `Doc.Stats` says when to rotate it.

## License

MIT. See LICENSE.
