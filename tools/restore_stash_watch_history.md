# Recover watched state after Stash file moves or rescans

`restore_stash_watch_history.py` imports **only recorded play timestamps**, never
ratings, resume positions, inferred watches, or synthetic dates. It reads supplied
Stash SQLite backups through temporary private copies (including a WAL when
present). It never modifies the supplied databases. Python's standard library is
sufficient.

The audit requires a current Stash GraphQL response saved as JSON, containing:

```graphql
{ findScenes(filter: {per_page: -1}) { scenes {
  id title date studio {name} stash_ids {endpoint stash_id}
  files {path size duration fingerprints {type value}}
  play_history
} } }
```

Use the existing Stash API key to request that snapshot; do not put credentials in
committed files or shell arguments. Run a dry audit first:

```sh
python3 tools/restore_stash_watch_history.py \
  --database /path/to/older-stash.sqlite \
  --database /path/to/newer-stash.sqlite \
  --current /private/current-scenes.json \
  --root /collections/hentaied \
  --root /collections/misc/other \
  --root /collections/misc/ws \
  --root /collections/misc/whisparr \
  --report /private/restore-plan.json
```

The script uses exact MD5 hashes, or matching OS hashes **with exact file size and
millisecond-rounded duration**. Stable external provider IDs also resolve moves,
but additionally require agreeing titles and release dates. When those are
unavailable, it accepts only a unique exact normalized title **together with the
same studio and release date**. Conflicting strong identities or multiple targets
are rejected. Numeric Stash IDs and titles alone are never used as proof.

Review the report, then add `--apply` with `STASH_URL` and `STASH_API_KEY` supplied
through the process environment. Every target identity and existing timestamp set
is checked again before its additive `sceneAddPlay` call. All backups are merged
and duplicate timestamps are omitted. No activity is deleted or decremented.
Do not run concurrently with another history importer.

An optional `--sources` JSON array accepts normalized records from JAVBeacon's
archive: `id`, `title`, `date`, `studio`, `files`, `stash_ids`, and `events` (play
timestamps). Missing identity evidence remains an exception; this tool cannot
reconstruct history that was never recorded or is absent from every backup.

After restoring, run Silo's Stash Metadata **watched-sync** task. Verify the actual
catalog flags; the task's admission acknowledgement alone is not completion.

Tests:

```sh
python3 -m unittest discover -s tools -p test_restore_stash_watch_history.py -v
```
