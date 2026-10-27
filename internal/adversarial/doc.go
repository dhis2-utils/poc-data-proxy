// Package adversarial holds the scenarios of docs/ADVERSARIAL-TESTING.md: clients work
// on the hub offline, central changes under them, the uplink returns, and the ledger
// and the plan's invariants are checked. They run against the real DHIS2 in
// HUB_MANIFEST; on the fake central, the scenarios it cannot model skip.
package adversarial
