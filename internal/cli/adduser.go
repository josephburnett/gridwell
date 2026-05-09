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
	fs := flag.NewFlagSet("adduser", flag.ContinueOnError)
	db := resolveDB(fs, "./ascent.db")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ascent adduser USERNAME [--db PATH]")
		return 2
	}
	username := fs.Arg(0)

	password, err := readPasswordTwice()
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

// readPasswordTwice prompts twice and returns the password if both match.
// Used for adduser; not used at login (where we just take what's posted).
func readPasswordTwice() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("password input requires a TTY")
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
