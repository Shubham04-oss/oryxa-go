//go:build !linux

package host

// undumpable is Linux's; elsewhere there is no /proc to protect.
func undumpable() error { return nil }
