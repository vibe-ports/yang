// SPDX-License-Identifier: BSD-3-Clause

package opidx

import "sync/atomic"

// Journal records the value of an index object right before each write to it, newest last
// (#240 step 4, design 13). Entries are typed values, not closures. Rollback(mark) replays the
// entries recorded after mark newest first, which returns every object to its value at mark.
// An index node or header is recorded once per epoch: an epoch never spans a mark (Mark,
// Rollback and Commit start a new one, from a process-wide counter), so a skipped write always
// follows an older entry of the same object in every tail that contains it, and no entry is
// ever dead. Map cells and counters are recorded at every write. While a
// journal is installed a journaled map never deletes a key on the forward path: a removal
// stores the nil tombstone (readers treat nil as absent), and Commit deletes the tombstones the
// log names. Replay assigns existing keys, deletes inserted ones and copies values, so it
// never allocates.
type Journal struct {
	entries []Entry
	charger Charger
	epoch   uint64
}

var epochs atomic.Uint64

func (j *Journal) newEpoch() { j.epoch = epochs.Add(1) }

// first reports whether the object with stamp is written for the first time in this epoch.
func (j *Journal) first(stamp *uint64) bool {
	if j.epoch == 0 {
		j.newEpoch()
	}
	if *stamp == j.epoch {
		return false
	}
	*stamp = j.epoch
	return true
}

// Charger pays for recorded entries: two units for an object image (its capture and its
// restoration), three for a map cell (and its purge visit).
type Charger interface{ ChargeEntries(units uint64) }

// Hooks connect an index structure to its owner: Work counts index work (visits, rotations,
// relabels), Journal returns the journal its writes are recorded in (nil: not recorded).
type Hooks interface {
	Work(units uint64)
	Journal() *Journal
}

// Entry is one recorded value. Its fields are interpreted by the object that recorded it.
type Entry struct {
	obj journaled
	had bool
	key any
	ptr [6]any
	num [3]uint64
}

type journaled interface {
	undo(e *Entry)
	purge(e *Entry)
}

// SetCharger sets who pays for recorded entries (nil: nobody).
func (j *Journal) SetCharger(c Charger) { j.charger = c }

// Mark returns the current log position for Rollback, and starts an epoch.
func (j *Journal) Mark() int {
	j.newEpoch()
	return len(j.entries)
}

// Len is the number of entries.
func (j *Journal) Len() int { return len(j.entries) }

func (j *Journal) add(e Entry, units uint64) {
	j.entries = append(j.entries, e)
	if j.charger != nil {
		j.charger.ChargeEntries(units)
	}
}

func (j *Journal) image(e Entry) { j.add(e, 2) }
func (j *Journal) cell(e Entry)  { j.add(e, 3) }

// Rollback restores every object written since mark and drops those entries.
func (j *Journal) Rollback(mark int) {
	for i := len(j.entries) - 1; i >= mark; i-- {
		j.entries[i].obj.undo(&j.entries[i])
	}
	clear(j.entries[mark:])
	j.entries = j.entries[:mark]
	j.newEpoch()
}

// Commit keeps every recorded write: it deletes the tombstones the log names (a recreated
// cell is live and stays) and empties the log, retaining its capacity.
func (j *Journal) Commit() {
	for i := range j.entries {
		j.entries[i].obj.purge(&j.entries[i])
	}
	clear(j.entries)
	j.entries = j.entries[:0]
	j.newEpoch()
}

// cellMap journals the cells of a map with pointer values.
type cellMap[K comparable, V any] map[K]*V

func (m cellMap[K, V]) undo(e *Entry) {
	k, _ := e.key.(K)
	if e.had {
		m[k], _ = e.ptr[0].(*V)
	} else {
		delete(m, k)
	}
}

func (m cellMap[K, V]) purge(e *Entry) {
	k, _ := e.key.(K)
	if v, ok := m[k]; ok && v == nil {
		delete(m, k)
	}
}

func recordCell[K comparable, V any](j *Journal, m map[K]*V, k K) {
	old, had := m[k]
	j.cell(Entry{obj: cellMap[K, V](m), key: k, had: had, ptr: [6]any{old}})
}

// SetCell stores m[k] = v, journaled. Without a journal it is the plain assignment.
func SetCell[K comparable, V any](j *Journal, m map[K]*V, k K, v *V) {
	if j != nil {
		recordCell(j, m, k)
	}
	m[k] = v
}

// DeleteCell removes m[k]: under a journal it records the cell and stores the nil tombstone.
func DeleteCell[K comparable, V any](j *Journal, m map[K]*V, k K) {
	if j == nil {
		delete(m, k)
		return
	}
	if _, ok := m[k]; !ok {
		return
	}
	recordCell(j, m, k)
	m[k] = nil
}

type intField int

func (p *intField) undo(e *Entry) { *p = intField(e.num[0]) } //nolint:gosec // the recorded int itself
func (p *intField) purge(*Entry)  {}
func (p *intField) record(j *Journal) {
	j.image(Entry{obj: p, num: [3]uint64{uint64(*p)}}) //nolint:gosec // restored bit for bit
}

// AddInt adds delta to *p, journaled.
func AddInt(j *Journal, p *int, delta int) {
	if j != nil {
		(*intField)(p).record(j)
	}
	*p += delta
}
