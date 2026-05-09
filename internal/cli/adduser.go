package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/josephburnett/ascent/internal/store"
	"golang.org/x/term"
)

// RunAddUser is the `ascent adduser USERNAME` subcommand. It opens the
// database (creating tables if they don't exist), then prompts for a
// password on stdin without echoing it.
func RunAddUser(args []string) int {
	return runAddUser(args, os.Stdin)
}

// RunAddUserWithStdin is a test entry point that lets a test inject a
// pre-baked password. The "stdin" arg is just any string to use as the
// password.
func RunAddUserWithStdin(args []string, password string) int {
	r, w, _ := pipe()
	go func() {
		_, _ = w.Write([]byte(password))
		_ = w.Close()
	}()
	return runAddUser(args, r)
}

// pipe is a thin wrapper for os.Pipe.
func pipe() (*os.File, *os.File, error) { return os.Pipe() }

func runAddUser(args []string, stdin *os.File) int {
	fs := flag.NewFlagSet("adduser", flag.ContinueOnError)
	db := resolveDB(fs, "./ascent.db")
	args = reorderFlagsFirst(args, func(name string) bool {
		// All current flags take a value; this function is the place
		// to special-case bool flags when they appear.
		return name == "db"
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ascent adduser USERNAME [--db PATH]")
		return 2
	}
	username := fs.Arg(0)

	password, err := readPasswordFrom(stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adduser: %v\n", err)
		return 1
	}

	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adduser: open db: %v\n", err)
		return 1
	}
	defer s.Close()

	u, err := s.CreateUser(context.Background(), username, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "adduser: %v\n", err)
		return 1
	}
	fmt.Printf("ascent: created user %s (id=%d, root_grid_id=%d)\n", u.Username, u.ID, u.RootGridID)
	return 0
}

// readPasswordFrom reads a password from the given file. If the file is a
// TTY it prompts and confirms; otherwise it reads one line of bytes. The
// non-TTY fallback is intentional: scripted provisioning is the dominant
// non-interactive use, and the pipe already implies the operator vetted
// the source.
func readPasswordFrom(in *os.File) (string, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		var p string
		_, err := fmt.Fscanln(in, &p)
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		if p == "" {
			return "", errors.New("password is empty")
		}
		return p, nil
	}
	fmt.Fprint(os.Stderr, "password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "confirm: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	if len(a) == 0 {
		return "", errors.New("password is empty")
	}
	return string(a), nil
}
