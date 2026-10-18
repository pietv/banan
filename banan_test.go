package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pietv/banan/fsm"
)

const (
	command = "testdata/command.sh"

	// fakePID is a PID of a banan instance that isn't this process.
	fakePID = 1 << 30
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// newBanan returns a banan instance logging into dir and
// running the test command that sleeps and exits
// with the exit code.
func newBanan(dir string, sleep time.Duration, exit int) *Banan {
	b := New(command,
		WithLogDir(dir),
		WithCmdArgs([]string{
			strconv.FormatFloat(sleep.Seconds(), 'f', -1, 64),
			strconv.Itoa(exit),
		}),
	)
	b.LogName = "test"
	return b
}

func states(b *Banan) []string {
	var ss []string
	for _, tr := range b.state.Log() {
		ss = append(ss, tr.Transition.To)
	}
	return ss
}

// checkStates compares the current Banan instance state log
// against a wanted list of states.
func checkStates(b *Banan, want ...string) error {
	if got := states(b); !slices.Equal(got, want) {
		return fmt.Errorf("got: %q,\n\nwant: %q", got, want)
	}
	return nil
}

// checkValid checks there were no invalid transitions.
func checkValid(b *Banan) error {
	var invalid []string
	for _, tr := range b.state.Log() {
		if tr.Status == fsm.Invalid {
			invalid = append(invalid, tr.Transition.String())
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid transitions: %q", invalid)
	}
	return nil
}

func writeLog(t *testing.T, dir string, recs ...Record) {
	t.Helper()

	f, err := os.OpenFile(filepath.Join(dir, "test.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for i := range recs {
		if err := enc.Encode(&recs[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func readLog(t *testing.T, dir string) []Record {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func ago(d time.Duration) RecordTime { return RecordTime(time.Now().UTC().Add(-d)) }

func deadPID(t *testing.T) int {
	t.Helper()

	cmd := exec.Command(command, "0", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestStart(t *testing.T) {
	tests := []struct {
		name string
		exit int
	}{
		{name: "success", exit: 0},
		{name: "failure", exit: 3},
	}

	for _, tt := range tests {
		dir := t.TempDir()
		b := newBanan(dir, 0, tt.exit)

		if err := b.Start(); err != nil {
			t.Errorf("%s: Start: %v", tt.name, err)
			continue
		}
		if code := b.ExitCode(); code != tt.exit {
			t.Errorf("%s: exit code: got %d, want %d", tt.name, code, tt.exit)
		}

		if err := checkValid(b); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
		if err := checkStates(b,
			"Read Log", "Find Queue Empty", "Mark Processing", "Mark Done",
			"Exit"); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}

		recs := readLog(t, dir)
		if len(recs) != 2 || recs[0].State != "Processing" || recs[1].State != "Done" {
			t.Errorf("%s: log: got %+v, want Processing, Done", tt.name, recs)
			continue
		}
		if done := recs[1]; done.PID != b.PID || done.ExitCode != tt.exit || (done.ErrorMessage != "") != (tt.exit != 0) {
			t.Errorf("%s: done record: got %+v", tt.name, done)
		}
	}
}

func TestStartCommandNotStarted(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		command  string
		wantExit int
	}{
		{name: "not found in PATH", command: "no-such-command", wantExit: 127},
		{name: "quoted command line", command: "echo a b c", wantExit: 127},
		{name: "no such path", command: "/no/such/command", wantExit: 127},
		{name: "not executable", command: notExecutable, wantExit: 126},
	}

	for _, tt := range tests {
		dir := t.TempDir()
		b := New(tt.command, WithLogDir(dir))
		b.LogName = "test"

		if err := b.Start(); err != nil {
			t.Errorf("%s: Start: %v", tt.name, err)
			continue
		}
		if code := b.ExitCode(); code != tt.wantExit {
			t.Errorf("%s: exit code: got %d, want %d", tt.name, code, tt.wantExit)
		}
		if b.startErr == nil {
			t.Errorf("%s: no start error recorded", tt.name)
		}

		if err := checkValid(b); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
		if err := checkStates(b,
			"Read Log", "Find Queue Empty", "Mark Processing", "Mark Done",
			"Exit"); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}

		// The failure is recorded in the log too.
		recs := readLog(t, dir)
		if done := recs[len(recs)-1]; done.State != "Done" || done.ExitCode != -1 || done.ErrorMessage == "" {
			t.Errorf("%s: done record: got %+v, want exit code -1 with an error message", tt.name, done)
		}
	}
}

func TestStartErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) *Banan
		wantErr string
	}{
		{
			name: "log directory doesn't exist",
			setup: func(t *testing.T, dir string) *Banan {
				return newBanan(filepath.Join(dir, "nope"), 0, 0)
			},
			wantErr: "no such file or directory",
		},
		{
			name: "corrupt log",
			setup: func(t *testing.T, dir string) *Banan {
				if err := os.WriteFile(filepath.Join(dir, "test.log"), []byte("garbage\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return newBanan(dir, 0, 0)
			},
			wantErr: "invalid character",
		},
		{
			name: "banner file doesn't exist",
			setup: func(t *testing.T, dir string) *Banan {
				// Someone else is running, so there's a banner to show.
				writeLog(t, dir, Record{Time: ago(0), State: "Processing", PID: os.Getpid()})
				b := newBanan(dir, 0, 0)
				b.PID = fakePID
				b.RemindBanner = filepath.Join(dir, "no-such-banner")
				return b
			},
			wantErr: "banner: open",
		},
	}

	for _, tt := range tests {
		b := tt.setup(t, t.TempDir())

		err := b.Start()
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: Start: got error %v, want one containing %q", tt.name, err, tt.wantErr)
		}
		if err != nil && strings.Contains(err.Error(), "banan.Start") {
			t.Errorf("%s: error has an internal prefix: %v", tt.name, err)
		}
	}
}

func TestGobbleUp(t *testing.T) {
	tests := []struct {
		name    string
		log     []Record
		wantErr error
		want    []string
		wantRec string // State of the record appended to the log, if any.
	}{
		{
			name: "processing timed out",
			log:  []Record{{Time: ago(time.Hour), State: "Processing", PID: os.Getpid()}},
			want: []string{
				"Read Record", "Expire Processing", "Read Log",
				"Find Queue Empty", "Mark Processing",
			},
			wantRec: "Processing",
		},
		{
			name:    "processing still running",
			log:     []Record{{Time: ago(0), State: "Processing", PID: os.Getpid()}},
			wantErr: ErrKeepWaiting,
			want: []string{
				"Read Record", "Keep Processing", "Read Log",
				"Find Alive", "Join Queue", "Mark Queued",
			},
			wantRec: "Queued",
		},
		{
			name: "processing killed",
			log:  []Record{{Time: ago(0), State: "Processing", PID: deadPID(t)}},
			want: []string{
				"Read Record", "Keep Processing", "Read Log",
				"Find Killed", "Mark Killed", "Find Queue Empty", "Mark Processing",
			},
			wantRec: "Processing",
		},
		{
			name: "queued stale",
			log:  []Record{{Time: ago(10 * defaultTimeout), State: "Queued", PID: 1}},
			want: []string{
				"Read Record", "Drop Record", "Read Log",
				"Find Queue Empty", "Mark Processing",
			},
			wantRec: "Processing",
		},
		{
			name: "my turn",
			log: []Record{
				{Time: ago(0), State: "Done", PID: 1},
				{Time: ago(0), State: "Queued", PID: fakePID},
			},
			want: []string{
				"Read Record", "Find Done", "Read Record", "Queue Record", "Read Log",
				"Run Command", "Mark Processing",
			},
			wantRec: "Processing",
		},
		{
			name: "someone else's turn, pickup not expired",
			log: []Record{
				{Time: ago(0), State: "Done", PID: 1},
				{Time: ago(0), State: "Queued", PID: 2},
			},
			wantErr: ErrKeepWaiting,
			want: []string{
				"Read Record", "Find Done", "Read Record", "Queue Record", "Read Log",
				"Yield Turn",
			},
		},
		{
			name: "someone else's turn, pickup expired",
			log: []Record{
				{Time: ago(time.Minute), State: "Done", PID: 1},
				{Time: ago(time.Minute), State: "Queued", PID: 2},
			},
			want: []string{
				"Read Record", "Find Done", "Read Record", "Queue Record", "Read Log",
				"Take Turn", "Mark Processing",
			},
			wantRec: "Processing",
		},
	}

	for _, tt := range tests {
		dir := t.TempDir()
		writeLog(t, dir, tt.log...)

		b := newBanan(dir, 0, 0)
		b.PID = fakePID

		if err := b.GobbleUp(); !errors.Is(err, tt.wantErr) {
			t.Errorf("%s: GobbleUp: got error %v, want %v", tt.name, err, tt.wantErr)
			continue
		}

		if err := checkValid(b); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
		if err := checkStates(b, tt.want...); err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}

		got := readLog(t, dir)
		switch {
		case tt.wantRec == "" && len(got) != len(tt.log):
			t.Errorf("%s: log: got %d records, want %d unchanged", tt.name, len(got), len(tt.log))
		case tt.wantRec != "":
			if last := got[len(got)-1]; last.State != tt.wantRec || last.PID != b.PID {
				t.Errorf("%s: last record: got %+v, want %q by %d", tt.name, last, tt.wantRec, b.PID)
			}
		}
	}
}

func TestGobbleUpStillWaiting(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, Record{Time: ago(0), State: "Processing", PID: os.Getpid()})

	b := newBanan(dir, 0, 0)
	b.PID = fakePID

	b.CheckEvery = 0
	for i := 0; i < 2; i++ {
		if err := b.GobbleUp(); !errors.Is(err, ErrKeepWaiting) {
			t.Fatalf("GobbleUp: got error %v, want %v", err, ErrKeepWaiting)
		}
		b.Wait()
	}

	if err := checkValid(b); err != nil {
		t.Error(err)
	}
	if err := checkStates(b,
		"Read Record", "Keep Processing", "Read Log",
		"Find Alive", "Join Queue", "Mark Queued", "Wait",
		"Read Record", "Keep Processing", "Read Record", "Queue Record", "Read Log",
		"Find Alive", "Keep Waiting", "Wait"); err != nil {
		t.Error(err)
	}

	if recs := readLog(t, dir); len(recs) != 2 {
		t.Errorf("got %d records, want 2 (Processing, Queued)", len(recs))
	}
}

func TestGobbleUpQueueRebuilt(t *testing.T) {
	const other = 2

	dir := t.TempDir()
	writeLog(t, dir,
		Record{Time: ago(3 * time.Minute), State: "Processing", PID: os.Getpid()},
		Record{Time: ago(2 * time.Minute), State: "Queued", PID: other},
	)

	b := newBanan(dir, 0, 0)
	b.PID = fakePID
	b.CheckEvery = 0

	// Join the queue behind the other instance and check again.
	for i := 0; i < 2; i++ {
		if err := b.GobbleUp(); !errors.Is(err, ErrKeepWaiting) {
			t.Fatalf("GobbleUp: got error %v, want %v", err, ErrKeepWaiting)
		}
		b.Wait()
	}

	// The running instance finishes, the other one has its turn.
	writeLog(t, dir,
		Record{Time: ago(0), State: "Done", PID: os.Getpid()},
		Record{Time: ago(0), State: "Processing", PID: other},
		Record{Time: ago(0), State: "Done", PID: other},
	)

	if err := b.GobbleUp(); err != nil {
		t.Fatalf("GobbleUp: got error %v, want my turn", err)
	}
	if err := checkValid(b); err != nil {
		t.Error(err)
	}
	if got := states(b); !slices.Contains(got, "Run Command") {
		t.Errorf("got %q, want Run Command", got)
	}
}

// TestStartQueued runs two instances: the second one has to wait
// until the first one finishes and then takes its turn.
func TestStartQueued(t *testing.T) {
	dir := t.TempDir()

	first := newBanan(dir, 500*time.Millisecond, 0)
	second := newBanan(dir, 0, 0)
	second.PID = fakePID
	second.CheckEvery = 20 * time.Millisecond
	second.RemindEvery = time.Hour

	firstErr := make(chan error, 1)
	go func() { firstErr <- first.Start() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, _ := os.ReadFile(filepath.Join(dir, "test.log")); strings.Contains(string(data), "Processing") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first instance never started processing")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}

	if err := checkValid(first); err != nil {
		t.Errorf("first: %v", err)
	}
	if err := checkValid(second); err != nil {
		t.Errorf("second: %v", err)
	}
	for _, s := range []string{"Join Queue", "Mark Queued", "Wait", "Find Done", "Run Command", "Mark Done"} {
		if !slices.Contains(states(second), s) {
			t.Errorf("second instance never reached %q: %q", s, states(second))
		}
	}

	var got []string
	for _, rec := range readLog(t, dir) {
		who := "First"
		if rec.PID == second.PID {
			who = "Second"
		}
		got = append(got, fmt.Sprintf("%s %s", who, rec.State))
	}
	want := []string{
		"First Processing",
		"Second Queued",
		"First Done",
		"Second Processing",
		"Second Done"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWithRemindEvery(t *testing.T) {
	tests := []struct {
		name  string
		every time.Duration
		want  time.Duration
	}{
		{name: "negative", every: -time.Second, want: time.Second},
		{name: "zero", every: 0, want: time.Second},
		{name: "sub-second", every: 500 * time.Millisecond, want: time.Second},
		{name: "second", every: time.Second, want: time.Second},
		{name: "longer", every: 7 * time.Second, want: 7 * time.Second},
	}

	for _, tt := range tests {
		b := New(command, WithRemindEvery(tt.every))
		if b.RemindEvery != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, b.RemindEvery, tt.want)
		}
	}
}
