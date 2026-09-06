//go:build !converge_debug

package converge

// debugBuild reports whether the converge_debug tag is on.
const debugBuild = false

func (d *Doc) noteTxEnter()             {}
func (d *Doc) noteTxExit()              {}
func (d *Doc) noteTxSuspend() uint64    { return 0 }
func (d *Doc) noteTxResume(prev uint64) {}
func (d *Doc) assertNotInTx()           {}
