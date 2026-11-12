package netsetup

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// SetTimeZone sets the Pi's time zone to central's (serverTimeZoneId), through
// timedatectl: the image allows the hub that one timedated action
// (polkit-1/rules.d/51-hub-timezone.rules). Unattended upgrades reboot at 03:00 in the
// system's time zone (apt.conf.d/20auto-upgrades); with central's zone that is the
// facility's night, not 03:00 UTC (10:00 in Laos). A central that runs on UTC leaves
// the Pi on UTC. A zone the Pi already has is left as it is.
func SetTimeZone(name string) {
	if _, err := time.LoadLocation(name); err != nil || name == "Local" {
		slog.Warn("time zone: central's zone is unknown here", "zone", name)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cur, err := exec.CommandContext(ctx, "timedatectl", "show", "--property=Timezone", "--value").Output()
	if err == nil && strings.TrimSpace(string(cur)) == name {
		return
	}
	if out, err := exec.CommandContext(ctx, "timedatectl", "set-timezone", name).CombinedOutput(); err != nil {
		slog.Warn("time zone: not set", "zone", name, "err", strings.TrimSpace(string(out)))
		return
	}
	slog.Info("time zone: set to central's", "zone", name)
}
