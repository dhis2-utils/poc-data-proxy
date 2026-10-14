# Working on the facility hub

## Keep the structure; add mechanisms only for measured problems

The hub's current structure is a baseline to keep, not to grow. Before adding a mechanism (a cache, a second code path, a special case, a background job), name the measured problem it solves: a test, a profile or a load run that shows it, and the numbers before and after. If the problem is not measured, measure it first; if the gain is small, leave the mechanism out.

Example: ordering a first pull "most recent first" across programs was built, then removed once shared listings made the first pull short enough (15 minutes at HC IV size) that its bookkeeping was not worth it.

Where complexity already lives, and why, is in `docs/PERFORMANCE.md` ("How the hub scales"). Change those parts with their guards:

- **Searches** (`internal/tracker/search.go`): part SQL, part Go, and both must mean exactly the same. The oracles (`internal/live`, against DHIS2), `TestQueryPlansUseIndexes` and `TestFirstPageReadsOnWithoutGaps`.
- **Caches**: paged results (`results.go`), decoded payloads (`payloads.go`), per-request lookups (`Viewer.memo`), prepared statements (`store/objects.go`). Each has one invalidation rule, written next to it.
- **Shared listings** (`internal/sync`, `access.List.GroupKey`): safe only if the group key covers everything central uses to decide visibility. The twin users in the access matrix test it.

## Before committing

- `go vet ./...` and `go test ./...`; `go test -race` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store`.
- Behaviour that central decides is checked against DHIS2 with `go run ./cmd/hubtest run -profile <synthetic|sierra-leone|laos> -version 42` (docs/TESTING.md).
- Performance claims come with numbers from `internal/perf` or `perf/pi.sh` (docs/PERFORMANCE.md).
