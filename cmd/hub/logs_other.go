//go:build !windows && !darwin

package main

// logToFile: on Linux the hub logs to the journal through standard error.
func logToFile(string) {}
