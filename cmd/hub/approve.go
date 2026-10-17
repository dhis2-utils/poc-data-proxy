package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dhis2/facility-hub/internal/hubaccount"
)

// approve gives hubs that asked for one an account of its own on central
// (docs/DEPLOYMENT.md, "The hub's own account"). Run by the national team, as a user
// allowed to create users, whenever new hubs are set up. Without hub IDs it lists the
// hubs waiting, with who set each up and any reason it may not have one; with hub IDs
// it approves those:
//
//	HUB_ADMIN_PASSWORD=... hub approve -central https://hmis.example.org -user admin \
//	    -group <hub accounts group> -role <hub account role> [hub ID ...]
func approve(args []string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	o := hubaccount.Options{}
	fs.StringVar(&o.Central, "central", "", "central DHIS2 URL")
	fs.StringVar(&o.Username, "user", "", "a user allowed to create users (password in HUB_ADMIN_PASSWORD)")
	fs.StringVar(&o.Group, "group", "", "UID of the user group of hub accounts (write sharing on the facility-hub namespaces)")
	fs.StringVar(&o.Role, "role", "", "UID of the user role hub accounts get (no authorities needed)")
	fs.Parse(args)
	o.Password = os.Getenv("HUB_ADMIN_PASSWORD")
	if o.Central == "" || o.Username == "" || o.Password == "" || o.Group == "" || o.Role == "" {
		return errors.New("usage: HUB_ADMIN_PASSWORD=... hub approve -central URL -user NAME -group UID -role UID [hub ID ...]")
	}
	ctx := context.Background()
	if fs.NArg() == 0 {
		pending, err := hubaccount.Pending(ctx, o)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("no hub waiting for an account")
		}
		for _, p := range pending {
			fmt.Printf("%s\t%s %s\tset up by %s at %s", p.HubID, p.Facility, p.FacilityName, p.SetupBy, p.SetupAt)
			if p.Problem != "" {
				fmt.Printf("\tNOT APPROVABLE: %s", p.Problem)
			}
			fmt.Println()
		}
		if len(pending) > 0 {
			fmt.Println("check each with the person who set it up, then: hub approve ... <hub ID> ...")
		}
		return nil
	}
	done, err := hubaccount.Approve(ctx, o, fs.Args())
	if len(done) > 0 {
		fmt.Printf("approved %d hub(s): %s\n", len(done), strings.Join(done, ", "))
	}
	return err
}
