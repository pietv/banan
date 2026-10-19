package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/integrii/flaggy"
)

func main() {
	var (
		timeout               = defaultTimeout
		queuedPickupAllowance = defaultQueuedPickupAllowance
		remindEvery           = defaultRemindEvery
		remindBanner          string

		logdir = "."
		env    []string
		debug  bool
		man    bool
	)

	flaggy.SetName("Banan")
	flaggy.SetDescription("a simple job queuer")
	flaggy.DefaultParser.ShowHelpOnUnexpected = false

	flaggy.DefaultParser.AdditionalHelpPrepend = "Usage: banan [FLAGS...] COMMAND"
	flaggy.Duration(&timeout, "", "timeout", "Period after which the command is considered timed out")
	flaggy.Duration(&remindEvery, "", "remind-every", "Remind that the command is still waiting to be executed")
	flaggy.Duration(&queuedPickupAllowance, "", "queued-pickup", "Allow the next queued command to start running if previous is slow to pick up")
	flaggy.String(&remindBanner, "", "remind-banner", "Show this file instead of standard message.")
	flaggy.String(&logdir, "", "log-dir", "Directory to create the log file in")
	flaggy.StringSlice(&env, "e", "environment", "Set environment variables.")
	flaggy.Bool(&debug, "d", "debug", "Debug operation")
	flaggy.Bool(&man, "", "man", "Show the manual page")

	// Undocumented: print the state diagram and exit.
	if slices.Contains(os.Args[1:], "--mermaid") {
		if err := New("banan").Mermaid(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "banan: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	flaggy.Parse()

	if man {
		if err := ShowMan(); err != nil {
			flaggy.ShowHelpAndExit("")
		}
		os.Exit(0)
	}

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
		WithCmdArgs(args),
		WithCmdEnv(env),
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
		// Forward signals to the child process.
		for sig := range ch {
			if b.Cmd != nil && b.Cmd.Process != nil {
				_ = b.Cmd.Process.Signal(sig)
			}
		}
	}()
	if err := b.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "banan: %v\n", err)
		os.Exit(1)
	}
	if b.startErr != nil {
		fmt.Fprintf(os.Stderr, "banan: %v\n", b.startErr)
	}
	os.Exit(b.ExitCode())
}
