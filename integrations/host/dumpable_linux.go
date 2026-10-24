package host

import "syscall"

// undumpable clears the process's dumpable flag, which makes /proc/<pid>/mem
// and /proc/<pid>/environ readable only with CAP_SYS_PTRACE.
func undumpable() error {
	const prSetDumpable = 4 // PR_SET_DUMPABLE
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
