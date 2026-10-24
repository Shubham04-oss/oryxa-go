// Command launch runs a connector as the connector user: it switches to uid
// and gid 65533 with no groups and execs its arguments. It alone holds
// CAP_SETUID and CAP_SETGID (Dockerfile), and the user is fixed here, so the
// service that runs it can make a process less privileged but never more.
package main

import (
	"fmt"
	"os"
	"syscall"
)

// connectorUser is the image's connector user (Dockerfile).
const connectorUser = 65533

func main() {
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: launch <connector> [args...]"))
	}
	if err := syscall.Setgroups(nil); err != nil {
		fail(fmt.Errorf("setgroups: %w", err))
	}
	if err := syscall.Setgid(connectorUser); err != nil {
		fail(fmt.Errorf("setgid: %w", err))
	}
	if err := syscall.Setuid(connectorUser); err != nil {
		fail(fmt.Errorf("setuid: %w", err))
	}
	// The connector has no file capabilities, so exec leaves it with none.
	fail(syscall.Exec(os.Args[1], os.Args[1:], os.Environ()))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "launch:", err)
	os.Exit(1)
}
