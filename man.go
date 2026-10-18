package main

import (
	_ "embed" // For the manual page.
	"errors"
	"os"
	"os/exec"
)

var (
	// ErrNoMan is returned when there's no utility to display the manual page.
	ErrNoMan = errors.New("neither mandoc(1) nor man(1) is available")

	//go:embed banan.1
	manpage []byte
)

// ShowMan displays the embedded manual page using the mandoc(1)
// utility or, if it's missing, the man(1) utility.
func ShowMan() error {
	var args []string
	if path, err := exec.LookPath("mandoc"); err == nil {
		args = []string{path, "-a"}
	} else if path, err := exec.LookPath("man"); err == nil {
		args = []string{path}
	} else {
		return ErrNoMan
	}

	// Both utilities want a file, and man(1) needs the path to have a slash.
	f, err := os.CreateTemp("", "banan-*.1")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(manpage); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	cmd := exec.Command(args[0], append(args[1:], f.Name())...) // #nosec G204
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
