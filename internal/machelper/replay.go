package machelper

// ReplayWindow is a sliding window over 64-bit counters: the highest accepted
// counter plus a 2048-bit ring of "seen" flags below it. Call Check before
// decrypting and Commit only after the packet authenticated. Not safe for
// concurrent use; Session serialises access.
type ReplayWindow struct {
	words   [ReplayWindowSize / 64]uint64
	highest uint64
	any     bool
}

const ReplayWindowSize = 2048

func windowBit(c uint64) (int, uint64) {
	i := c % ReplayWindowSize
	return int(i / 64), 1 << (i % 64)
}

func (w *ReplayWindow) Check(counter uint64) bool {
	if !w.any || counter > w.highest {
		return true
	}
	if w.highest-counter >= ReplayWindowSize {
		return false
	}
	i, m := windowBit(counter)
	return w.words[i]&m == 0
}

// Commit records the counter and reports whether it was acceptable.
func (w *ReplayWindow) Commit(counter uint64) bool {
	if !w.Check(counter) {
		return false
	}
	switch {
	case !w.any:
		w.any = true
		w.highest = counter
	case counter > w.highest:
		if counter-w.highest >= ReplayWindowSize {
			w.words = [ReplayWindowSize / 64]uint64{}
		} else {
			for c := w.highest + 1; c <= counter; c++ {
				i, m := windowBit(c)
				w.words[i] &^= m
			}
		}
		w.highest = counter
	}
	i, m := windowBit(counter)
	w.words[i] |= m
	return true
}
