// Package fsm is a simple finite state machine package for tracking
// valid and invalid state transitions.
package fsm

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Transition holds the current and the next states.
type Transition struct{ From, To string }

// String returns a string representation of state transition.
func (t Transition) String() string {
	return fmt.Sprintf("%s → %s", t.From, t.To)
}

// Status is mark of a valid or an invalid transition.
type Status int

const (
	Invalid Status = iota + 1
	Valid
)

// String returns a string for ‘Valid’ or ‘Invalid’ status.
func (v Status) String() string {
	switch v {
	case Valid:
		return "Valid"
	case Invalid:
		return "Invalid"
	default:
		return "(Status: unknown)"
	}
}

// Trace is a line in the log that saves a transition
// along with its validity for further analysis.
type Trace struct {
	Status     Status
	Transition Transition
}

// State is a simple state machine that tracks state transitions
// as strings along with valid state chart and a log of
// past state changes.
//
// State machine operations are thread-safe.
type State struct {
	current  string
	chart    map[Transition]struct{}
	log      []Trace
	callback func(Trace, []Trace)

	sync.Mutex
}

// New returns a new state machine. It requires an initial (start) state,
// and an optional pairs of allowed state transitions:
//
// For example:
//
//	states := fsm.New("START",
//	                    "START", "B",
//	                    "B", "C",
//	                    "C", "FINISH")
//
// The allowed state transitions are optional. If they're not supplied
// all state transitions are considered valid.
func New(start string, tt ...string) *State {
	if len(tt)%2 != 0 {
		panic("valid transitions are pairs (from → to) so their total number must be even")
	}

	chart := make(map[Transition]struct{})
	for i := 0; i < len(tt); i += 2 {
		chart[Transition{From: tt[i], To: tt[i+1]}] = struct{}{}
	}

	return &State{
		current: start,
		chart:   chart,
	}
}

// To transitions from the current state to a new state. The
// transition is recorded in the log. If the transition is
// invalid and a dedicated callback is registered with the
// ‘RegisterCallback()’ method, the callback is executed.
//
// To returns the state machine in a new state, so multiple
// ‘To(...)’ statements may be chained, for example:
//
//	state.To("B").To("C").To("FINISH")
func (s *State) To(newState string) *State {
	s.Lock()

	status := Invalid
	if _, ok := s.chart[Transition{s.current, newState}]; ok || len(s.chart) == 0 {
		status = Valid
	}
	trace := Trace{status, Transition{From: s.current, To: newState}}
	s.log = append(s.log, trace)
	s.current = newState

	var callback func(Trace, []Trace)
	var log []Trace
	if status == Invalid && s.callback != nil {
		callback = s.callback
		log = slices.Clone(s.log)
	}

	s.Unlock()

	// Invoke the callback after releasing the lock.
	// Pass a copy of the log, not the original.
	if callback != nil {
		callback(trace, log)
	}
	return s
}

// RegisterCallback allows to register a function that will
// be called when an invalid state transition has occurred.
// The callback is supplied with the invalid transition
// log entry and a copy of the full log.
func (s *State) RegisterCallback(f func(Trace, []Trace)) {
	s.Lock()
	defer s.Unlock()

	s.callback = f
}

// Current returns the current state machine state.
func (s *State) Current() string {
	s.Lock()
	defer s.Unlock()

	return s.current
}

// Chart returns the state machine allowed's transitions
// as a map.
func (s *State) Chart() map[Transition]struct{} {
	s.Lock()
	defer s.Unlock()

	// Return a copy of the transitions chart.
	return maps.Clone(s.chart)
}

// Log returns the state machine's log.
func (s *State) Log() []Trace {
	s.Lock()
	defer s.Unlock()

	// Return a copy of the log.
	return slices.Clone(s.log)
}
