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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pietv/banan/internal/lockedfile"
)

var (
	ErrKeepWaiting   = errors.New("keep waiting on command completion")
	ErrProcessKilled = errors.New("process killed")
)

const (
	timestamp = "2006-01-02 15:03:04 MST"

	defaultTimeout               = 30 * time.Minute
	defaultRelevanceTimeout      = 8 * defaultTimeout
	defaultQueuedPickupAllowance = 15 * time.Second
	defaultCheckEvery            = 1 * time.Second
	defaultRemindEvery           = 5 * time.Second
)

type (
	RecordTime     time.Time
	RecordDuration time.Duration
)

type Record struct {
	Time  RecordTime `json:"time"`
	State string     `json:"state"`
	PID   int        `json:"pid"`

	ExitCode     int    `json:"exit_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	SystemTime   string `json:"system_time,omitempty"`
	UserTime     string `json:"user_time,omitempty"`
}

func (rt *RecordTime) UnmarshalJSON(b []byte) error {
	t, err := time.Parse(timestamp, strings.Trim(string(b), `"`))
	if err != nil {
		return fmt.Errorf("RecordTime.UnmarshalJSON: %v", err)
	}
	*rt = RecordTime(t)
	return nil
}

func (rd *RecordDuration) UnmarshalJSON(b []byte) error {
	duration, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("RecordDuration.UnmarshalJSON: %v", err)
	}
	*rd = RecordDuration(duration)
	return nil
}

func (rd *RecordDuration) MarshalJSON() (out []byte, _ error) { //nolint:unparam// yes, the error is always ‘nil’.
	return strconv.AppendInt(out, int64(*rd), 10), nil
}

func (rt *RecordTime) MarshalJSON() (out []byte, err error) {
	out = make([]byte, 0, len(timestamp)+len(`""`))
	out = append(out, '"')
	out = time.Time(*rt).AppendFormat(out, timestamp)
	out = append(out, '"')
	if err != nil {
		return nil, errors.New("RecordTime.MarshalJSON: " + err.Error())
	}
	return out, nil
}

type Banan struct {
	PID          int
	Command      string
	Args         []string
	Env          []string
	RemindBanner string
	LogDir       string
	LogName      string

	Timeout               time.Duration
	RelevanceTimeout      time.Duration
	QueuedPickupAllowance time.Duration
	CheckEvery            time.Duration
	RemindEvery           time.Duration

	Queue  Queue
	Queued map[int]struct{}
	Cmd    *exec.Cmd

	lockedlogfile *lockedfile.File
}

func New(cmd string, options ...func(*Banan)) *Banan {
	logname := func(cmd string) string {
		return strings.TrimSuffix(filepath.Base(cmd), filepath.Ext(cmd))
	}

	banan := &Banan{
		Command:               cmd,
		PID:                   os.Getpid(),
		Timeout:               defaultTimeout,
		RelevanceTimeout:      8 * defaultTimeout,
		QueuedPickupAllowance: defaultQueuedPickupAllowance,
		RemindEvery:           defaultRemindEvery,
		CheckEvery:            defaultCheckEvery,
		LogDir:                os.TempDir(),
		LogName:               logname(cmd),

		Queue:  make([]*Waiting, 0),
		Queued: make(map[int]struct{}),
	}
	for _, o := range options {
		o(banan)
	}
	return banan
}

func WithArgs(args []string) func(*Banan) {
	return func(b *Banan) {
		b.Args = args
	}
}

func WithEnv(env []string) func(*Banan) {
	return func(b *Banan) {
		b.Env = env
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

func WithRemindEvery(every time.Duration) func(*Banan) {
	return func(b *Banan) {
		b.RemindEvery = every
	}
}

func WithRemindBanner(banner string) func(*Banan) {
	return func(b *Banan) {
		b.RemindBanner = banner
	}
}

// Start starts the process of executing a command.
// It either proceeds to executing it right away or
// waits upon completion of its one or more of its
// other instances; it consults the log to know about
// them.
func (b *Banan) Start() error {
	for err := b.GobbleUp(); err != nil; err = b.GobbleUp() {
		switch {
		case errors.Is(err, ErrKeepWaiting):
			// Show banner.
			if err := b.ShowWaitingBanner(); err != nil {
				return fmt.Errorf("banan.Start: banner %v", err)
			}

			// Wait it out.
			b.Wait()
		default:
			return fmt.Errorf("banan.Start: %v", err)
		}
	}

	b.Cmd = exec.Command(b.Command, b.Args...) // #nosec G204
	b.Cmd.Env = b.Env
	errbuf := new(bytes.Buffer)
	b.Cmd.Stdout = os.Stdout
	b.Cmd.Stderr = io.MultiWriter(os.Stdout, errbuf)
	err := b.Cmd.Run()
	return b.WithLock(func(b *Banan) error {
		if err != nil {
			return b.MarkDone(b.Cmd, err, fmt.Errorf("%v", errbuf.String()))
		}
		return b.MarkDone(b.Cmd)
	})
}

// Wait puts everything to sleep until the next log check.
func (b *Banan) Wait() {
	time.Sleep(b.CheckEvery)
}

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

	if errclose := f.Close(); errclose != nil {
		log.Printf("unlocking %v failed: %v", logfile, err)
	} else {
		log.Printf("unlocked %v", logfile)
	}
	b.lockedlogfile = nil

	return err
}

func (b *Banan) GobbleUp() error {
	return b.WithLock(func(b *Banan) error {
		var (
			scanner = bufio.NewScanner(b.lockedlogfile)

			waitingPID  int
			waitingDone RecordTime
		)

		for scanner.Scan() {
			rec, err := b.RecordParse(scanner.Text())
			if scanner.Err() != nil || err != nil {
				return err
			}

			switch rec.State {
			case "Done":
				waitingPID, waitingDone = 0, rec.Time
			case "Processing":
				// Timed out?
				if time.Since(time.Time(rec.Time)) < b.Timeout {
					// No.
					waitingPID = rec.PID
				} else {
					// Yes.
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
				if time.Since(time.Time(rec.Time)) < b.RelevanceTimeout {
					b.Queued[rec.PID] = struct{}{}
					heap.Push(&b.Queue, &Waiting{
						PID:      rec.PID,
						Inserted: rec.Time,
					})
				}
			}
		}

		// Not waiting on anything and nothing's queued.
		if waitingPID == 0 && len(b.Queue) == 0 {
			return b.MarkProcessing()
		}

		// Not waiting on anyone.
		if waitingPID == 0 {
			top := heap.Pop(&b.Queue).(*Waiting)

			// My turn.
			if top.PID == b.PID {
				return b.MarkProcessing()
			}

			// Not my turn, pickup time isn't expired. Keep waiting.
			if time.Since(time.Time(waitingDone)) <= b.QueuedPickupAllowance {
				return ErrKeepWaiting
			}

			// Not my turn, pickup time is expired. Proceed.
			return b.MarkProcessing()
		}

		// Waiting on a job completion.
		if _, ok := b.Queued[b.PID]; !ok {
			// Let others know I'm waiting in the queue too. Keep waiting.
			if err := b.MarkQueued(); err != nil {
				return err
			}
			return ErrKeepWaiting
		}
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
	if _, err := b.lockedlogfile.Seek(0, os.SEEK_END); err != nil { //nolint:staticcheck// this is not ‘os’ package
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

	log.Printf("   marking Done: %v", b.PID)
	rec := &Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Done",
		PID:   b.PID,

		ExitCode:     exitcode,
		ErrorMessage: message,
	}
	if cmd.ProcessState != nil {
		rec.SystemTime = fmt.Sprintf("%v", cmd.ProcessState.SystemTime())
		rec.UserTime = fmt.Sprintf("%v", cmd.ProcessState.UserTime())
	}
	err := json.NewEncoder(b.lockedlogfile).Encode(&rec)
	log.Printf("   ... marked")
	return err
}

// MarkProcessing marks in the log that the current global state
// is running this command line binary.
func (b *Banan) MarkProcessing() error {
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
	log.Printf("   marking Queued: %v", b.PID)
	err := json.NewEncoder(b.lockedlogfile).Encode(&Record{
		Time:  RecordTime(time.Now().UTC()),
		State: "Queued",
		PID:   b.PID,
	})
	log.Printf("   ... marked")
	return err
}
