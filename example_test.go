package converge_test

import (
	"fmt"

	"github.com/maxxu123456/converge"
)

// syncTo hands dst everything src holds that dst does not, which is what
// syncproto does with a connection in between.
func syncTo(src, dst *converge.Doc) {
	if err := dst.ApplyUpdate(src.EncodeStateAsUpdate(dst.StateVector()), "remote"); err != nil {
		fmt.Println("apply:", err)
	}
}

// Two replicas edit the same sentence without seeing each other, then swap
// updates and agree.
func Example() {
	a := converge.NewDocWith(converge.Options{ClientID: 1})
	b := converge.NewDocWith(converge.Options{ClientID: 2})
	at, bt := a.Text("body"), b.Text("body")

	a.Transact(nil, func(tx *converge.Tx) { tx.Insert(at, 0, "hello world") })
	syncTo(a, b)

	a.Transact(nil, func(tx *converge.Tx) { tx.Insert(at, 5, ",") })
	b.Transact(nil, func(tx *converge.Tx) { tx.Insert(bt, 11, "!") })
	syncTo(a, b)
	syncTo(b, a)

	fmt.Println(at)
	fmt.Println(bt)
	// Output:
	// hello, world!
	// hello, world!
}

// An editor binding applies the delta to whatever it draws.
func ExampleText_Observe() {
	d := converge.NewDocWith(converge.Options{ClientID: 1})
	body := d.Text("body")
	cancel := body.Observe(func(ev converge.Event) {
		fmt.Println(ev.Local, ev.Delta)
	})
	defer cancel()

	d.Transact(nil, func(tx *converge.Tx) { tx.Insert(body, 0, "hello world") })
	d.Transact(nil, func(tx *converge.Tx) {
		tx.Delete(body, 0, 5)
		tx.Insert(body, 0, "goodbye")
	})
	// Output:
	// true [insert "hello world"]
	// true [insert "goodbye" delete 5]
}

// A cursor sent as bytes still points at the rune it was parked on, even
// though the index it had moved.
func ExampleDoc_Resolve() {
	a := converge.NewDocWith(converge.Options{ClientID: 1})
	b := converge.NewDocWith(converge.Options{ClientID: 2})
	at, bt := a.Text("body"), b.Text("body")
	a.Transact(nil, func(tx *converge.Tx) { tx.Insert(at, 0, "hello world") })
	syncTo(a, b)

	blob, err := bt.Position(6, converge.AssocAfter).MarshalBinary()
	if err != nil {
		fmt.Println("marshal:", err)
		return
	}
	a.Transact(nil, func(tx *converge.Tx) { tx.Insert(at, 0, "oh, ") })

	var cursor converge.Position
	if err := cursor.UnmarshalBinary(blob); err != nil {
		fmt.Println("unmarshal:", err)
		return
	}
	text, index, ok := a.Resolve(cursor)
	fmt.Println(text.Name(), index, ok)
	fmt.Println(at.Slice(index, index+5))
	// Output:
	// body 10 true
	// world
}
