package fsm

import (
	"slices"
	"testing"
)

func TestNoChart(t *testing.T) {
	s := New("IDLE")

	s.To("RUNNING")
	s.To("RUNNING")
	s.To("DONE")

	for _, trace := range s.Log() {
		if trace.Status != Valid {
			t.Errorf("transition %s: got %s, want %s", trace.Transition, trace.Status, Valid)
		}
	}
}

func TestCurrent(t *testing.T) {
	s := New("IDLE",
		"IDLE", "RUNNING",
		"RUNNING", "DONE")

	if got, want := s.Current(), "IDLE"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	s.To("RUNNING")
	if got, want := s.Current(), "RUNNING"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	s.To("NOWHERE")
	if got, want := s.Current(), "NOWHERE"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToChaining(t *testing.T) {
	s := New("IDLE",
		"IDLE", "RUNNING",
		"RUNNING", "DONE")

	s.To("RUNNING").To("DONE")

	if got, want := s.Current(), "DONE"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got := s.Log()
	want := []Trace{
		{Valid, Transition{From: "IDLE", To: "RUNNING"}},
		{Valid, Transition{From: "RUNNING", To: "DONE"}},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNewUnevenTransitionsPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	New("IDLE",
		"IDLE", "RUNNING", "RUNNING")
}

func TestWithoutCallback(t *testing.T) {
	s := New("IDLE",
		"IDLE", "RUNNING",
		"RUNNING", "DONE").To("DONE")

	got := s.Log()
	if len(got) != 1 {
		t.Fatalf("got %d log entries, want 1", len(got))
	}
	if got[0].Status != Invalid {
		t.Errorf("transition %q: got %s, want %s", got[0].Transition, got[0].Status, Invalid)
	}
}

func TestWithCallback(t *testing.T) {
	s := New("IDLE",
		"IDLE", "RUNNING",
		"RUNNING", "DONE").To("DONE")

	var (
		got    Trace
		called bool
	)
	s.RegisterCallback(func(trace Trace, log []Trace) {
		called = true
		got = trace
	})

	s.To("RUNNING")
	if !called {
		t.Fatal("expected callback to be invoked")
	}

	want := Trace{Status: Invalid, Transition: Transition{From: "DONE", To: "RUNNING"}}
	if got != want {
		t.Errorf("got trace %v, want %v", got, want)
	}

	called = false
	s.To("DONE")
	if called {
		t.Fatal("expected callback not to be invoked")
	}
}
