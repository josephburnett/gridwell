// Package cli implements the subcommand dispatch for the ascent binary.
//
// Each subcommand returns the process exit code so main.go is a thin shell.
package cli

import "fmt"

// RunInit creates the database and schema. Implemented in init.go.
func RunInit(args []string) int {
	fmt.Println("init: not yet implemented")
	return 0
}

// RunAddUser creates a user. Implemented in adduser.go.
func RunAddUser(args []string) int {
	fmt.Println("adduser: not yet implemented")
	return 0
}

// RunServe starts the HTTP server. Implemented in serve.go.
func RunServe(args []string) int {
	fmt.Println("serve: not yet implemented")
	return 0
}
