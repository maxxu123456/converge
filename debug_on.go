//go:build converge_debug

package converge

import (
	"runtime"
	"strconv"
)

// debugBuild reports whether the converge_debug tag is on.
const debugBuild = true

// goid parses this goroutine's id out of its stack header. Only debug builds
// pay for it, which is the only reason such a thing is acceptable here.
func goid() uint64 {
	var buf [40]byte
	s := string(buf[:runtime.Stack(buf[:], false)])
	s = s[len("goroutine "):]
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			s = s[:i]
			break
		}
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func (d *Doc) noteTxEnter() { d.debugOwner.Store(goid()) }
func (d *Doc) noteTxExit()  { d.debugOwner.Store(0) }

// noteTxSuspend clears the owner for a window where the lock is released, and
// returns what to put back. Observers run in that window and may call back in.
func (d *Doc) noteTxSuspend() uint64 { return d.debugOwner.Swap(0) }

func (d *Doc) noteTxResume(prev uint64) { d.debugOwner.Store(prev) }

// assertNotInTx turns the deadlock you would otherwise get into a named panic.
func (d *Doc) assertNotInTx() {
	if o := d.debugOwner.Load(); o != 0 && o == goid() {
		panic(&UsageError{Msg: "method called on a Doc or Text from inside its own Transact callback, use the tx instead"})
	}
}
