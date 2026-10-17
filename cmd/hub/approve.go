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

// approve gives each hub that asked for one an account of its own on central
// (docs/DEPLOYMENT.md, "The hub's own account"). Run by the national team, as a user
// allowed to create users, whenever new hubs are set up:
//
//	HUB_ADMIN_PASSWORD=... hub approve -central https://hmis.example.org -user admin \
//	    -group <hub accounts group> -role <hub account role>
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
		return errors.New("usage: HUB_ADMIN_PASSWORD=... hub approve -central URL -user NAME -group UID -role UID")
	}
	done, err := hubaccount.Approve(context.Background(), o)
	if len(done) > 0 {
		fmt.Printf("approved %d hub(s): %s\n", len(done), strings.Join(done, ", "))
	} else if err == nil {
		fmt.Println("no hub waiting for an account")
	}
	return err
}
