# Working on the facility hub

## Keep the structure; add mechanisms only for measured problems

The hub's current structure is a baseline to keep, not to grow. Before adding a mechanism (a cache, a second code path, a special case, a background job), name the measured problem it solves: a test, a profile or a load run that shows it, and the numbers before and after. If the problem is not measured, measure it first; if the gain is small, leave the mechanism out.

Example: ordering a first pull "most recent first" across programs was built, then removed once shared listings made the first pull short enough (15 minutes at HC IV size) that its bookkeeping was not worth it.

Where complexity already lives, and why, is in `docs/PERFORMANCE.md` ("How the hub scales"). Change those parts with their guards:

- **Searches** (`internal/tracker/search.go`): part SQL, part Go, and both must mean exactly the same. The oracles (`internal/live`, against DHIS2), `TestQueryPlansUseIndexes` and `TestFirstPageReadsOnWithoutGaps`.
- **Caches**: decoded payloads (`payloads.go`), per-request lookups (`Viewer.memo`), prepared statements (`store/objects.go`), and where each user's last page ended (`pagePositions`, only a row identifier). Each has one invalidation rule, written next to it. A cache of paged results was removed once every page stopped reading at its end: it hid a full read per page. Guards for read-on paging: `TestPagesReadOnFromLastPosition` and `TestQueryPlansUseIndexes`.
- **Shared listings** (`internal/sync`, `access.List.GroupKey`): safe only if the group key covers everything central uses to decide visibility. The twin users in the access matrix test it.
- **Aggregate forms** (`internal/sync/aggregate.go`, `internal/aggregateapi`): what is served offline must match central's forms, and edits must reach central as their author. `TestAggregateOracle`, the `TestAggregate*` scenarios and `TestAggregateDataEntryApp`.

## Before committing

- `go vet ./...` and `go test ./...`; `go test -race` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store`.
- Behaviour that central decides is checked against DHIS2 with `go run ./cmd/hubtest run -profile <synthetic|sierra-leone|laos> -version 42` (docs/TESTING.md).
- Performance claims come with numbers from `internal/perf` or `perf/pi.sh` (docs/PERFORMANCE.md).
- A data migration that touches existing rows (a new index, a rewritten column) runs within SQLite's 4 GB, with sorts in memory: time it at the `hc4-5y` size (4.6 million objects; `internal/perf.TestRetentionMigration` shows how) before committing. A full index rebuild ran out of memory there; a hub that cannot migrate stays on its old release (docs/DEPLOYMENT.md, "Upgrades").
