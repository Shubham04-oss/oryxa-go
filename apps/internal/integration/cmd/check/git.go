package main

import (
	"bytes"
	"os/exec"
	"strings"
)

// git runs git with the arguments given, and answers what it printed.
// Nothing is sent to stderr: a failure here is the caller saying which ref it
// could not find, and a reviewer's terminal is not where that belongs.
func git(command ...string) ([]byte, error) { return run("", command...) }

// gitIn is git run in one directory, which is how a checkout that is not this
// one is still measured against its own base.
func gitIn(dir string, command ...string) ([]byte, error) { return run(dir, command...) }

func run(dir string, command ...string) ([]byte, error) {
	args := command
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	built := exec.Command("git", args...)
	var out bytes.Buffer
	built.Stdout = &out
	built.Stderr = &bytes.Buffer{}
	if err := built.Run(); err != nil {
		return nil, &gitError{command: command, err: err}
	}
	return out.Bytes(), nil
}

type gitError struct {
	command []string
	err     error
}

func (e *gitError) Error() string {
	return "git " + strings.Join(e.command, " ") + ": " + e.err.Error()
}

func (e *gitError) Unwrap() error { return e.err }
