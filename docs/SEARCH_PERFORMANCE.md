# Search and filter performance

The performance changes preserve existing search fields, wildcard AND/OR logic,
reverse performer-name lookup, stable ordering and complete bulk selections.
They do not change playback, downloads or library ownership.

## Measured changes

These are local synthetic benchmarks, not production response-time guarantees.

| Operation | Synthetic catalog | Before | After |
| --- | --- | --- | --- |
| Label suggestions · SQLite | 12,000 releases, 32 sites, four site links per release | 62 ms | 13 ms |
| Select all matching IDs · SQLite | 12,000 releases | 330 ms / 64 MB allocated | 8 ms / 0.55 MB allocated |
| Metadata text search count · SQLite | 12,000 releases with performer and tag rows | 53 ms | 23 ms |
| Selective director suggestions · PostgreSQL 18 | 50,000 releases, 50 matching rows | 24 ms | 0.12 ms |

SQLite results use repeated Go benchmark iterations. Allocations describe Go
heap allocation per operation, not the process's resident memory limit.
PostgreSQL results use `EXPLAIN (ANALYZE, BUFFERS)` in a disposable local database;
the new index changed a sequential scan to a bitmap index scan. Query cost and
benefit depend on catalog size, selectivity, cache warmth and hardware.

## Why it is faster

- Text search computes matching related-metadata IDs independently of the outer
  release row, allowing reusable membership sets and existing text indexes.
- Bulk selection fetches only IDs in one query, retaining the same filters and
  ordering as full release reads. Alternate store implementations retain the
  previous paginated fallback.
- Label suggestions check whether each site has any linked release instead of
  joining and grouping the site's title once for every release link.
- Director category filters search `LOWER(director)`. The new PostgreSQL index
  covers that exact expression; the existing raw-column index continues to
  support other search paths.
- Superseded browser requests cannot hide a newer request's loading overlay or
  trigger unnecessary metadata-suggestion refreshes. Failed active loads clear
  their overlay in a `finally` block.

The new index is installed by the normal PostgreSQL migration on application
startup. Its initial build uses database time and disk space. No global memory,
connection-pool or PostgreSQL resource settings are changed by this update.

## Reproduce and verify

Use disposable state and synthetic data. From the repository root:

```sh
# -mod=readonly avoids relying on an out-of-date local vendor directory.
go test -mod=readonly ./internal/store -run '^$' \
  -bench 'BenchmarkRelease(LabelSuggestions|SelectionIDs|TextSearch)$' -benchmem
node --test internal/web/release_load_test.js
node --check internal/web/static/app.js
go test -mod=readonly ./cmd/... ./internal/...
go vet -mod=readonly ./cmd/... ./internal/...
```

`TestPostgresSearchAndSelection` is an opt-in integration test. Use the existing
`JAVBEACON_TEST_PG_*` test connection variables and set
`JAVBEACON_TEST_PG_SEARCH=1` only for a **fresh disposable database** whose name
ends in `_test`. Never point these tests at the application database. The test
seeds synthetic releases and verifies selection, ordering, metadata search,
label suggestions and installation of the director expression index.
