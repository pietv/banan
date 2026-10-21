package main

import (
	"bufio"
	"bytes"
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pietv/banan/fsm"
	"github.com/pietv/banan/internal/lockedfile"
)

var (
	ErrKeepWaiting   = errors.New("keep waiting on command completion")
	ErrProcessKilled = errors.New("process killed")
)

const (
	timestamp = "2006-01-02 15:04:05 MST"

	defaultTimeout               = 30 * time.Minute
	defaultRelevanceTimeout      = 4 * time.Hour
	defaultQueuedPickupAllowance = 15 * time.Second
	defaultCheckEvery            = 1 * time.Second
	defaultRemindEvery           = 5 * time.Second
)

type RecordTime time.Time

type Record struct {
	Time  RecordTime `json:"time"`
	State string     `json:"state"`
	PID   int        `json:"pid"`

	ExitCode      int    `json:"exit_code,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
	WallClockTime string `json:"wall_clock_time,omitempty"`
}

func (rt *RecordTime) UnmarshalJSON(b []byte) error {
	t, err := time.ParseInLocation(timestamp, strings.Trim(string(b), `"`), time.UTC)
	if err != nil {
		return fmt.Errorf("json.unmarshal.RecordTime: %v", err)
	}
	*rt = RecordTime(t)
	return nil
}

func (rt *RecordTime) MarshalJSON() (out []byte, _ error) {
	out = make([]byte, 0, len(timestamp)+len(`""`))
	out = append(out, '"')
	out = time.Time(*rt).AppendFormat(out, timestamp)
	out = append(out, '"')

	return
}

type Banan struct {
	PID          int
	Command      string
	CmdArgs      []string
	CmdEnv       []string
	RemindBanner string
	LogDir       string
	LogName      string

	Timeout               time.Duration
	RelevanceTimeout      time.Duration
	QueuedPickupAllowance time.Duration
	CheckEvery            time.Duration
	RemindEvery           time.Duration

	Queue   Queue
	Queued  map[int]struct{}
	Cmd     *exec.Cmd
	Started time.Time

	lockedlogfile *lockedfile.File

	// startErr is set if the command could not be started at all
	// (not found, not executable), as opposed to running and failing.
	startErr error
	state    *fsm.State
}

func New(cmd string, options ...func(*Banan)) *Banan {
	logname := func(cmd string) string {
		return strings.TrimSuffix(filepath.Base(cmd), filepath.Ext(cmd))
	}

	banan := &Banan{
		Command:               cmd,
		PID:                   os.Getpid(),
		Timeout:               defaultTimeout,
		RelevanceTimeout:      defaultRelevanceTimeout,
		QueuedPickupAllowance: defaultQueuedPickupAllowance,
		RemindEvery:           defaultRemindEvery,
		CheckEvery:            defaultCheckEvery,
		LogDir:                os.TempDir(),
		LogName:               logname(cmd),

		Queue:  make([]*Waiting, 0),
		Queued: make(map[int]struct{}),

		state: fsm.New("Start",
			// Reading the log record by record, either for
			// the first time or after waiting.
			"Start", "Read Record",
			"Start", "Read Log",
			"Wait", "Read Record",
			"Wait", "Read Log",
			"Read Record", "Read Log",
			"Read Record", "Find Done",
			"Read Record", "Keep Processing",
			"Read Record", "Expire Processing",
			"Read Record", "Queue Record",
			"Read Record", "Drop Record",
			"Find Done", "Read Record",
			"Find Done", "Read Log",
			"Keep Processing", "Read Record",
			"Keep Processing", "Read Log",
			"Expire Processing", "Read Record",
			"Expire Processing", "Read Log",
			"Queue Record", "Read Record",
			"Queue Record", "Read Log",
			"Drop Record", "Read Record",
			"Drop Record", "Read Log",

			// Deciding whether to proceed or keep waiting.
			"Read Log", "Find Killed",
			"Read Log", "Find Alive",
			"Read Log", "Find Queue Empty",
			"Read Log", "Run Command",
			"Read Log", "Yield Turn",
			"Read Log", "Take Turn",
			"Find Killed", "Mark Killed",
			"Mark Killed", "Find Queue Empty",
			"Mark Killed", "Run Command",
			"Mark Killed", "Yield Turn",
			"Mark Killed", "Take Turn",
			"Find Alive", "Join Queue",
			"Find Alive", "Keep Waiting",
			"Find Queue Empty", "Mark Processing",
			"Run Command", "Mark Processing",
			"Take Turn", "Mark Processing",
			"Join Queue", "Mark Queued",

			// Waiting, running the command, exiting.
			"Mark Queued", "Show Banner",
			"Mark Queued", "Wait",
			"Yield Turn", "Show Banner",
			"Yield Turn", "Wait",
			"Keep Waiting", "Show Banner",
			"Keep Waiting", "Wait",
			"Show Banner", "Wait",
			"Mark Processing", "Mark Done",
			"Mark Done", "Exit",
		),
	}
	for _, o := range options {
		o(banan)
	}
	return banan
}

func WithCmdArgs(args []string) func(*Banan) {
	return func(b *Banan) {
		b.CmdArgs = args
	}
}

func WithCmdEnv(env []string) func(*Banan) {
	return func(b *Banan) {
		b.CmdEnv = env
	}
}

func WithTimeout(timeout time.Duration) func(*Banan) {
	return func(b *Banan) {
		b.Timeout = timeout
	}
}

func WithQueuedPickupAllowance(allowance time.Duration) func(*Banan) {
	return func(b *Banan) {
		b.QueuedPickupAllowance = allowance
	}
}

func WithLogDir(logdir string) func(*Banan) {
	return func(b *Banan) {
		b.LogDir = logdir
	}
}

// WithRemindEvery sets how often to remind about waiting. Anything
// shorter than a second is set to a second: the reminder is scheduled
// by whole seconds of the Unix time.
func WithRemindEvery(every time.Duration) func(*Banan) {
	return func(b *Banan) {
		b.RemindEvery = max(every, time.Second)
	}
}

func WithRemindBanner(banner string) func(*Banan) {
	return func(b *Banan) {
		b.RemindBanner = banner
	}
}

// Start starts the process of executing a command.
// It either proceeds to executing it right away or
// waits upon completion of one or more of its other
// instances; it consults the log periodically
// to be updated about the current status and if
// it's “our” turn to execute the command.
func (b *Banan) Start() error {
	for err := b.GobbleUp(); err != nil; err = b.GobbleUp() {
		switch {
		case errors.Is(err, ErrKeepWaiting):
			// Show banner.
			if err := b.ShowWaitingBanner(); err != nil {
				return fmt.Errorf("banner: %w", err)
			}

			// Wait it out.
			b.Wait()
		default:
			return err
		}
	}

	b.Cmd = exec.Command(b.Command, b.CmdArgs...) // #nosec G204
	b.Cmd.Env = append(os.Environ(), b.CmdEnv...)
	errbuf := new(bytes.Buffer)
	b.Cmd.Stdout = os.Stdout
	b.Cmd.Stderr = io.MultiWriter(os.Stdout, errbuf)
	b.Started = time.Now()
	err := b.Cmd.Run()
	if err != nil && b.Cmd.ProcessState == nil {
		b.startErr = err
	}
	lockerr := b.WithLock(func(b *Banan) error {
		if err != nil {
			return b.MarkDone(b.Cmd, err, fmt.Errorf("%v", errbuf.String()))
		}
		return b.MarkDone(b.Cmd)
	})
	b.state.To("Exit")
	return lockerr
}

// ExitCode returns the exit code of the command that has been run or zero.
// If the command could not be started, it is 127 if the command was not
// found and 126 otherwise, same as in a shell.
func (b *Banan) ExitCode() int {
	switch {
	case b.startErr != nil:
		if errors.Is(b.startErr, exec.ErrNotFound) || errors.Is(b.startErr, fs.ErrNotExist) {
			return 127
		}
		return 126
	case b.Cmd != nil && b.Cmd.ProcessState != nil:
		return b.Cmd.ProcessState.ExitCode()
	}
	return 0
}

// Wait puts everything to sleep until the next log check.
func (b *Banan) Wait() { b.state.To("Wait"); time.Sleep(b.CheckEvery) }

// ShowWaitingBanner displays a waiting banner reminding the user
// that the command still cannot be executed or the
// default stock message.
func (b *Banan) ShowWaitingBanner() error {
	if time.Now().UTC().Unix()%(int64(b.RemindEvery/time.Second)) != 0 {
		return nil
	}

	banner := []byte(`
------------------------------------------
Waiting on a running command to complete.
------------------------------------------
`)
	if b.RemindBanner != "" {
		var err error
		banner, err = os.ReadFile(b.RemindBanner)
		if err != nil {
			return err
		}
	}
	b.state.To("Show Banner")
	return template.Must(template.New("banner").Parse(string(banner))).Execute(os.Stdout, b)
}

func (b *Banan) WithLock(fn func(b *Banan) error) error {
	logfile := filepath.Join(b.LogDir, b.LogName+".log")

	f, err := lockedfile.Edit(logfile)
	if err != nil {
		log.Printf("locking %v failed: %v", logfile, err)
		return err
	}

	log.Printf("locked   %v", logfile)
	b.lockedlogfile = f

	err = fn(b)

	b.lockedlogfile = nil

	if err := f.Sync(); err != nil {
		log.Printf("syncing %v failed: %v", logfile, err)
	} else {
		log.Printf("syncing %v", logfile)
	}

	if err := f.Close(); err != nil {
		log.Printf("unlocking %v failed: %v", logfile, err)
	} else {
		log.Printf("unlocked %v", logfile)
	}

	return err
}

func (b *Banan) GobbleUp() error {
	return b.WithLock(func(b *Banan) error {
		var (
			scanner = bufio.NewScanner(b.lockedlogfile)

			waitingPID  int
			waitingDone RecordTime
		)

		// The whole log is read anew every time, so start
		// with an empty queue rather than piling up duplicates.
		b.Queue = b.Queue[:0]
		clear(b.Queued)

		for scanner.Scan() {
			b.state.To("Read Record")
			rec, err := b.RecordParse(scanner.Text())
			if scanner.Err() != nil || err != nil {
				return err
			}

			switch rec.State {
			case "Done", "Killed":
				log.Printf("   %q: %v", "Done", time.Time(rec.Time).Format(timestamp))
				b.state.To("Find Done")
				waitingPID, waitingDone = 0, rec.Time
			case "Processing":
				log.Printf("   %q: %v, timeout=%v", "Processing", time.Time(rec.Time).Format(timestamp), time.Duration(b.Timeout))
				// Timed out?
				if time.Since(time.Time(rec.Time)) < b.Timeout {
					// No.
					b.state.To("Keep Processing")
					log.Printf("   ... not timed out")
					waitingPID = rec.PID
				} else {
					// Yes.
					b.state.To("Expire Processing")
					log.Printf("   ... timed out")
					waitingPID = 0
				}

				// Remove PID from the waiting queue.
				delete(b.Queued, rec.PID)
				if len(b.Queue) > 0 {
					item := heap.Pop(&b.Queue).(*Waiting)
					if item.PID != rec.PID {
						heap.Push(&b.Queue, item)
					}
				}
			case "Queued":
				// Don't queue very old “Queued” records.
				log.Printf("   %q: %v", "Queued", time.Time(rec.Time).Format(timestamp))
				if time.Since(time.Time(rec.Time)) < b.RelevanceTimeout {
					b.state.To("Queue Record")
					b.Queued[rec.PID] = struct{}{}
					heap.Push(&b.Queue, &Waiting{
						PID:      rec.PID,
						Inserted: rec.Time,
					})
				} else {
					b.state.To("Drop Record")
				}
			}
		}
		b.state.To("Read Log")

		// Check the process waited upon is still running.
		if waitingPID != 0 && b.Killed(waitingPID) {
			log.Printf("   process killed")
			if err := b.MarkKilled(waitingPID); err != nil {
				return err
			}
			waitingPID = 0
		}

		// Not waiting on anything and nothing's queued.
		if waitingPID == 0 && len(b.Queue) == 0 {
			b.state.To("Find Queue Empty")
			log.Printf("   proceeding: queue is empty")
			return b.MarkProcessing()
		}

		// Not waiting on anyone.
		if waitingPID == 0 {
			top := heap.Pop(&b.Queue).(*Waiting)

			// My turn.
			if top.PID == b.PID {
				b.state.To("Run Command")
				log.Printf("   proceeding: my turn")
				return b.MarkProcessing()
			}

			// Not my turn, pickup time isn't expired. Keep waiting.
			if time.Since(time.Time(waitingDone)) <= b.QueuedPickupAllowance {
				b.state.To("Yield Turn")
				log.Printf("   waiting: not my turn")
				return ErrKeepWaiting
			}

			// Not my turn, pickup time is expired. Proceed.
			b.state.To("Take Turn")
			log.Printf("   proceeding: someone's turn forfeited")
			return b.MarkProcessing()
		}

		// Waiting on a job completion.
		if _, ok := b.Queued[b.PID]; !ok {
			// Let others know I'm waiting in the queue too. Keep waiting.
			b.state.To("Join Queue")
			if err := b.MarkQueued(); err != nil {
				return err
			}
			log.Printf("   waiting: joined the queue")
			return ErrKeepWaiting
		}
		b.state.To("Keep Waiting")
		log.Printf("   waiting: still")
		return ErrKeepWaiting
	})
}

// RecordParse translates the JSON representation of the Record
// into a Record instance.
func (b *Banan) RecordParse(text string) (rec Record, err error) {
	err = json.NewDecoder(strings.NewReader(text)).Decode(&rec)
	return
}

// MarkDone logs the completion of the executed command.
func (b *Banan) MarkDone(cmd *exec.Cmd, errs ...error) error {
	// Append to the end of the file.
	if _, err := b.lockedlogfile.Seek(0, io.SeekEnd); err != nil { //nolint:staticcheck// this is not ‘os’ package
		return err
	}

	var (
		exitcode = cmd.ProcessState.ExitCode()
		message  string
	)
	switch len(errs) {
	case 1:
		message = fmt.Sprintf("%v", errs[0])
	case 2:
		message = fmt.Sprintf("%v; %v", errs[0], errs[1])
	default:
	}

	b.state.To("Mark Done")
	log.Printf("   marking Done: %v", b.PID)
	rec := &Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Done",
		PID:   b.PID,

		ExitCode:      exitcode,
		ErrorMessage:  message,
		WallClockTime: fmt.Sprintf("%.2fs", time.Since(b.Started).Seconds()),
	}
	err := json.NewEncoder(b.lockedlogfile).Encode(&rec)
	log.Printf("   ... marked")
	return err
}

// MarkKilled logs the waited upon process is not running anymore.
func (b *Banan) MarkKilled(pid int) error {
	// Append to the end of the file.
	if _, err := b.lockedlogfile.Seek(0, io.SeekEnd); err != nil { //nolint:staticcheck// this is not ‘os’ package
		return err
	}

	b.state.To("Mark Killed")
	log.Printf("   marking Killed: %v", pid)
	rec := &Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Killed",
		PID:   pid,

		ExitCode:     -1,
		ErrorMessage: fmt.Sprintf("%v", ErrProcessKilled),
	}
	err := json.NewEncoder(b.lockedlogfile).Encode(&rec)
	log.Printf("   ... marked")
	return err
}

// MarkProcessing marks in the log that the current global state
// is running this command line binary.
func (b *Banan) MarkProcessing() error {
	b.state.To("Mark Processing")
	log.Printf("   marking Processing: %v", b.PID)
	err := json.NewEncoder(b.lockedlogfile).Encode(&Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Processing",
		PID:   b.PID,
	})
	log.Printf("   ... marked")
	return err
}

// MarkQueued marks in the log that the current global state
// is waiting upon completion of the previous command.
func (b *Banan) MarkQueued() error {
	b.state.To("Mark Queued")
	log.Printf("   marking Queued: %v", b.PID)
	err := json.NewEncoder(b.lockedlogfile).Encode(&Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Queued",
		PID:   b.PID,
	})
	log.Printf("   ... marked")
	return err
}

// Killed checks if a running process is not running anymore.
func (b *Banan) Killed(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		b.state.To("Find Killed")
		return true
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		b.state.To("Find Killed")
		return true
	}
	b.state.To("Find Alive")
	return false
}
