package main

import (
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/integrii/flaggy"
)

func main() {
	var (
		timeout               = defaultTimeout
		remindEvery           = defaultRemindEvery
		remindBanner          string
		queuedPickupAllowance = defaultQueuedPickupAllowance

		logdir = os.TempDir()
		env    []string
		debug  bool
	)

	flaggy.SetName("Banan")
	flaggy.SetDescription("a simple job queuer")
	flaggy.DefaultParser.ShowHelpOnUnexpected = false

	flaggy.DefaultParser.AdditionalHelpPrepend = "Usage: banan [FLAGS...] COMMAND"
	flaggy.Duration(&timeout, "", "timeout", "Period after which the command is considered timed out")
	flaggy.Duration(&remindEvery, "", "remind-every", "Remind that the command is still waiting to be executed")
	flaggy.Duration(&queuedPickupAllowance, "", "--queued-pickup", "Allow the next queued command to start running if previous is slow to pick up")
	flaggy.String(&remindBanner, "", "remind-banner", "Show this file instead of standard message.")
	flaggy.String(&logdir, "", "log-dir", "Directory to create the log file in")
	flaggy.StringSlice(&env, "e", "environment", "Set environment variables.")
	flaggy.Bool(&debug, "d", "debug", "Debug operation")

	flaggy.Parse()

	if len(flaggy.TrailingArguments) == 0 {
		flaggy.ShowHelpAndExit("You must specify a command to run")
	}

	// Command.
	cmd := flaggy.TrailingArguments[0]

	// Command arguments.
	var args []string
	if len(flaggy.TrailingArguments) > 1 {
		args = flaggy.TrailingArguments[1:]
	}

	log.SetPrefix("banan: ")
	log.SetFlags(log.Ltime)
	if !debug {
		log.SetOutput(io.Discard)
	}

	b := New(cmd,
		WithArgs(args),
		WithEnv(env),
		WithLogDir(logdir),
		WithTimeout(timeout),
		WithRemindEvery(remindEvery),
		WithRemindBanner(remindBanner),
		WithQueuedPickupAllowance(queuedPickupAllowance),
	)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch,
		syscall.SIGHUP, syscall.SIGINT,
		syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		// Handle incoming signals.
		<-ch
		if b.Cmd == nil {
			os.Exit(1)
		}

		p, err := os.FindProcess(b.Cmd.Process.Pid)
		if err != nil {
			os.Exit(1)
		}

		_ = p.Kill()
		_ = b.WithLock(func(b *Banan) error {
			return b.MarkDone(b.Cmd, ErrProcessKilled)
		})
		os.Exit(1)
	}()
	if err := b.Start(); err != nil {
		log.Fatal(err)
	}
	os.Exit(b.ExitCode())
}
