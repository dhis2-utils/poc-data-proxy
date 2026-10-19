//go:build !windows

package main

import "errors"

func service(args []string) error {
	if _, err := parseServiceArgs(args); err != nil {
		return err
	}
	return errors.New("hub service: Windows only (on Linux, systemd runs `hub serve`)")
}
