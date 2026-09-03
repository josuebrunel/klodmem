# klodmem

[![CI](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml/badge.svg)](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml)

Full-text search over Claude Code's auto-memory, across every project.

Claude Code writes memory files to `~/.claude/projects/<project>/memory/*.md` and only lets itself find them again through `MEMORY.md`'s one-line index entries, matched by exact keyword. Ask about "port conflicts" when the note says "docker-compose mapping" and you get nothing — and each project's memories are invisible from every other project.

klodmem watches those same markdown files, indexes their frontmatter and body into a local SQLite database (FTS5 full-text search, in WAL mode), and exposes a `search_memory` tool over MCP so Claude Code can search the full content of every memory, in every project, not just the index line. The markdown files stay the source of truth — klodmem only reads them.

Optionally (opt-in, see below), it can also index your raw conversation history — every session transcript, across every project — and expose a second `search_history` tool for finding past discussions that never made it into memory at all.

## Install

Requires Go 1.25+.

```sh
go install github.com/josuebrunel/klodmem/cmd/klodmem@latest
```

This puts a `klodmem` binary in `$(go env GOPATH)/bin` — make sure that directory is on your `PATH`.

Alternatively, download a prebuilt binary from the [Releases page](https://github.com/josuebrunel/klodmem/releases) (Linux, macOS, and Windows, amd64/arm64), or clone and build locally:

```sh
git clone https://github.com/josuebrunel/klodmem.git
cd klodmem
make build   # produces ./bin/klodmem
```

## Register it with Claude Code

Add it once as a user-scoped MCP server so it's available in every project:

```sh
claude mcp add --scope user klodmem -- klodmem
```

(If you built locally instead of using `go install`, point at the binary instead: `claude mcp add --scope user klodmem -- /path/to/klodmem/bin/klodmem`.)

Start (or restart) a Claude Code session and you're done — Claude can now call `search_memory` to search across all your projects' memory files.

Verify it's connected:

```sh
claude mcp get klodmem
```

## How it works

On startup klodmem scans `~/.claude/projects/*/memory/*.md` (skipping each project's `MEMORY.md` index file itself), parses the YAML frontmatter (`name`, `description`, `metadata.type`) and body of every memory, and indexes it into a SQLite FTS5 table. It then keeps watching those directories for changes for as long as the MCP session is open, so memories written mid-session are searchable immediately.

The index lives at `~/.claude/klodmem/klodmem.db` by default and is safe to delete — klodmem rebuilds it from the markdown files on next start.

## Configuration

All settings are optional environment variables:

| Variable                | Default                      | Meaning                                   |
|--------------------------|-------------------------------|--------------------------------------------|
| `KLODMEM_MEMORY_ROOT`    | `~/.claude/projects`           | Root directory to scan for `*/memory/*.md` |
| `KLODMEM_DB_PATH`        | `~/.claude/klodmem/klodmem.db` | SQLite index file location                 |
| `KLODMEM_LOG_LEVEL`      | `info`                         | `debug`, `info`, `warn`, or `error`        |
| `KLODMEM_INDEX_HISTORY`  | `false`                        | Set `true` to also index conversation history and enable `search_history` (see below) |

Pass them via `-e` when registering, e.g.:

```sh
claude mcp add --scope user klodmem -e KLODMEM_LOG_LEVEL=debug -- klodmem
```

### One-shot indexing (`-ingest`)

```sh
klodmem -ingest
```

Runs a single scan of `KLODMEM_MEMORY_ROOT` into the index and exits — it doesn't start the MCP server or the live file watchers. Also indexes conversation history if `KLODMEM_INDEX_HISTORY=true`. Useful for warming the index right after installing, verifying indexing works, or running it periodically from cron independent of any Claude Code session.

## The `search_memory` tool

| Field     | Type   | Required | Description                                             |
|-----------|--------|----------|-----------------------------------------------------------|
| `query`   | string | yes      | Search terms                                             |
| `project` | string | no       | Restrict to one project's memory directory (slug form)   |
| `type`    | string | no       | Restrict to `user`, `feedback`, `project`, or `reference` |
| `limit`   | int    | no       | Max results (default 10)                                 |

Each result includes the matching project, type, name, description, a snippet of the matched text, and the file path — so Claude can read the full memory file for complete context.

## Conversation history search (opt-in)

`search_memory` only covers curated memory — the things Claude decided were worth writing down. Most of a conversation isn't that: it's the actual debugging, the code you pasted, the back-and-forth that never got distilled into a memory file. Set `KLODMEM_INDEX_HISTORY=true` and klodmem will also index every session transcript (`~/.claude/projects/<project>/<session-id>.jsonl`) and expose a `search_history` tool for it, so you can find past conversations directly ("did I already debug this exact error").

This is off by default because it's a different scope than memory search, worth knowing before turning it on:

- **It indexes more, and rawer, content.** Only the authored text of user/assistant turns is extracted — never tool input/output, file contents, thinking blocks, or images — but that's still your literal typed messages and Claude's literal responses, unfiltered by the curation `search_memory` relies on.
- **It's a separate, larger index.** Transcripts are typically much bigger than memory files; expect the SQLite index to grow accordingly, and expect a one-time cost the first time it scans your existing history (a few seconds per few hundred MB, incremental after that).
- **Subagent transcripts aren't indexed** — only each session's own top-level transcript.

### The `search_history` tool

| Field     | Type   | Required | Description                                    |
|-----------|--------|----------|--------------------------------------------------|
| `query`   | string | yes      | Search terms                                    |
| `project` | string | no       | Restrict to one project (slug form)             |
| `role`    | string | no       | Restrict to `user` or `assistant`                |
| `limit`   | int    | no       | Max results (default 10)                        |

Each result includes the project, role, timestamp, a snippet of the matched text, the session ID, and the transcript file path.

## Development

```sh
make build   # compile ./bin/klodmem
make run     # build and run
make test    # run the test suite
make lint    # go vet + golangci-lint
make tidy    # go mod tidy
```
