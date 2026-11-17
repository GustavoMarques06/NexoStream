package dvr

import (
	"sync"
	"time"
)

// State is the in-memory state of the DVR of one Media Node:
//
//   - the open hour files (last indexed piece and size), so each new piece
//     costs one append and no index read, and writes to an hour are serialized;
//   - which disks failed recently, so writes go to the healthy ones;
//   - how many MP4 exports run.
//
// Each Media Node agent owns one State and puts it in its Storage values
// (Storage.State). A Storage without one uses a process-wide default, which
// keeps small tools and tests simple. Two States must not record into the
// same folder.
type State struct {
	hoursMu sync.Mutex
	hours   map[string]*hourState // key: hour .ts path

	healthMu sync.Mutex
	down     map[string]time.Time

	jobsMu sync.Mutex
	jobs   int
}

// NewState returns an empty State.
func NewState() *State {
	return &State{hours: map[string]*hourState{}, down: map[string]time.Time{}}
}

// defaultState serves Storage values that carry no State of their own.
var defaultState = NewState()

// state returns the State a Storage works with.
func (s Storage) state() *State {
	if s.State != nil {
		return s.State
	}
	return defaultState
}

// forget drops the cached state of an hour file (as after a restart).
func (st *State) forget(ts string) {
	st.hoursMu.Lock()
	delete(st.hours, ts)
	st.hoursMu.Unlock()
}

// retryDownAfter is how long a failed disk is avoided before trying it again.
const retryDownAfter = time.Minute

// DiskDown reports whether a disk is currently considered failed.
func (st *State) DiskDown(d string) bool {
	st.healthMu.Lock()
	defer st.healthMu.Unlock()
	t, ok := st.down[d]
	if ok && time.Since(t) > retryDownAfter {
		delete(st.down, d)
		return false
	}
	return ok
}

func (st *State) markDown(d string, down bool) {
	st.healthMu.Lock()
	defer st.healthMu.Unlock()
	if down {
		st.down[d] = time.Now()
	} else {
		delete(st.down, d)
	}
}

// DiskDown reports whether a disk is considered failed by the default State.
func DiskDown(d string) bool { return defaultState.DiskDown(d) }

// acquireExport reserves one of the export slots of the node.
func (st *State) acquireExport(limit int) bool {
	st.jobsMu.Lock()
	defer st.jobsMu.Unlock()
	if st.jobs >= limit {
		return false
	}
	st.jobs++
	return true
}

func (st *State) releaseExport() {
	st.jobsMu.Lock()
	st.jobs--
	st.jobsMu.Unlock()
}
