//go:build !windows

package main

// logToFile: on Linux the hub logs to the journal through standard error.
func logToFile(string) {}
