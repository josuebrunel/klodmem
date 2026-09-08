# klodmem

[![CI](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml/badge.svg)](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml)

Full-text search over Claude Code's auto-memory and raw conversation history, across every project.

## What it gives you

Claude Code writes memory files to `~/.claude/projects/<project>/memory/*.md`, but can only find them again through `MEMORY.md`'s one-line index entries, matched by exact keyword. Ask about "port conflicts" when the note says "docker-compose mapping" and you get nothing, and each project's memories are invisible from every other project. klodmem fixes that:

- **Searches the full content of every memory, in every project.** Not just the `MEMORY.md` index line. Exposed to Claude as a `search_memory` tool over MCP.
- **Searches raw conversation history too.** Every session transcript, across every project, so past discussions that never made it into memory are findable via `search_history`.
- **Markdown stays the source of truth.** klodmem only reads your files and indexes them into a local SQLite database (FTS5 full-text search, WAL mode).
- **Live updates.** File watchers keep memories and new conversation turns searchable while a Claude Code session is open.

## Table of contents

- [Quick start](#quick-start)
- [Other ways to install](#other-ways-to-install)
- [How it works](#how-it-works)
- [CLI reference](#cli-reference)
- [Configuration](#configuration)
- [MCP tools](#mcp-tools)
- [Development](#development)

## Quick start

Requires Go 1.25+.

1. **Install.** This puts a `klodmem` binary in `$(go env GOPATH)/bin`, so make sure that directory is on your `PATH`.

   ```sh
   go install github.com/josuebrunel/klodmem/cmd/klodmem@latest
   ```

2. **Register it once as a user-scoped MCP server**, so it's available in every project:

   ```sh
   claude mcp add --scope user klodmem -- klodmem
   ```

3. **Restart Claude Code.** New sessions can now call `search_memory` and `search_history`.

4. **Verify it's connected:**

   ```sh
   claude mcp get klodmem
   ```

That's it. Next time you're in Claude Code, just ask: *"search my memories for docker-compose port conflicts"* or *"have I debugged this error before?"*

## Other ways to install

- **Prebuilt binaries** for Linux, macOS, and Windows (amd64/arm64) from the [Releases page](https://github.com/josuebrunel/klodmem/releases).
- **Build from source:**

  ```sh
  git clone https://github.com/josuebrunel/klodmem.git
  cd klodmem
  make build   # produces ./bin/klodmem
  ```

  If you built locally, point MCP at the binary when registering: `claude mcp add --scope user klodmem -- /path/to/klodmem/bin/klodmem`.

## How it works

On startup klodmem scans `~/.claude/projects/*/memory/*.md` (skipping each project's `MEMORY.md` index file), parses the YAML frontmatter and body of every memory, and indexes it into a SQLite FTS5 table. It also scans every `~/.claude/projects/*/<session-id>.jsonl` transcript, incrementally (only newly-appended lines each scan). Both keep watching for changes for as long as the MCP session is open.

The index lives at `~/.claude/klodmem/klodmem.db` by default. It's safe to delete: klodmem rebuilds it from your markdown files on next start.

## CLI reference

Running `klodmem` with no flags starts the MCP server, which is the normal way to use it. The flags below run one-off jobs instead:

| Command               | Does what                                                                        |
|------------------------|----------------------------------------------------------------------------------|
| `klodmem`              | Start the MCP server (scan then watch, with live search)                        |
| `klodmem -ingest`      | Index memory and history once, then exit                                         |
| `klodmem -ingest.memory` | Index memory files only, then exit                                             |
| `klodmem -ingest.history` | Index conversation history only, then exit                                    |
| `klodmem -stat`        | Print index statistics and exit, no scanning                                     |
| `klodmem -version`     | Print the klodmem version and exit                                               |

One-shot ingest is handy for warming the index right after installing, verifying indexing works, or running periodically from cron independent of any Claude Code session.

`-stat` prints a summary of what's currently indexed. Combine it with an ingest flag to refresh then report in one command, e.g. `klodmem -ingest -stat`:

```
klodmem index stats
  db: /home/user/.claude/klodmem/klodmem.db (5.9 MiB)

memory
  files: 1

history
  messages: 6398 (assistant: 5402, user: 996)
  sessions: 104
  projects: 22
  range: 2026-08-04T05:03:49.522Z to 2026-09-03T11:18:58.758Z

by project
  -home-user-workspace-project-alpha     2436 messages    24 sessions
  -home-user-workspace-project-beta       894 messages    13 sessions
  ...
```

## Configuration

All settings are optional environment variables:

| Variable                | Default                      | Meaning                                   |
|--------------------------|-------------------------------|--------------------------------------------|
| `KLODMEM_MEMORY_ROOT`    | `~/.claude/projects`           | Root directory to scan for `*/memory/*.md` |
| `KLODMEM_DB_PATH`        | `~/.claude/klodmem/klodmem.db` | SQLite index file location                 |
| `KLODMEM_LOG_LEVEL`      | `info`                         | `debug`, `info`, `warn`, or `error`        |

Pass them via `-e` when registering:

```sh
claude mcp add --scope user klodmem -e KLODMEM_LOG_LEVEL=debug -- klodmem
```

## MCP tools

### `search_memory`

Full-text search over every project's memory files.

| Field     | Type   | Required | Description                                             |
|-----------|--------|----------|-----------------------------------------------------------|
| `query`   | string | yes      | Search terms                                             |
| `project` | string | no       | Restrict to one project's memory directory (slug form)   |
| `type`    | string | no       | Restrict to `user`, `feedback`, `project`, or `reference` |
| `limit`   | int    | no       | Max results (default 10)                                 |

Each result includes the matching project, type, name, description, a snippet of the matched text, and the file path, so Claude can read the full memory file for complete context.

### `search_history`

`search_memory` only covers curated memory, the things Claude decided were worth writing down. Most of a conversation isn't that: the actual debugging, the code you pasted, the back-and-forth that never got distilled into a file. klodmem also indexes every session transcript and exposes `search_history` for it, so past conversations are directly findable.

Full-text search over raw session transcripts across every project.

| Field     | Type   | Required | Description                                    |
|-----------|--------|----------|--------------------------------------------------|
| `query`   | string | yes      | Search terms                                    |
| `project` | string | no       | Restrict to one project (slug form)             |
| `role`    | string | no       | Restrict to `user` or `assistant`                |
| `limit`   | int    | no       | Max results (default 10)                        |

Each result includes the project, role, timestamp, a snippet of the matched text, the session ID, and the transcript file path.

A few differences from memory search worth knowing:

- **Rawner content.** Only the authored text of user/assistant turns is extracted, never tool input/output, file contents, thinking blocks, or images. But that's your literal typed messages and Claude's literal responses, unfiltered by curation.
- **A bigger index.** Transcripts are typically much larger than memory files, so expect the SQLite index to grow accordingly. The first scan of existing history has a one-time cost (a few seconds per few hundred MB); after that it's incremental.
- **No subagent transcripts.** Only each session's own top-level transcript is indexed.

## Development

```sh
make build   # compile ./bin/klodmem
make run     # build and run
make test    # run the test suite
make lint    # go vet + golangci-lint
make tidy    # go mod tidy
```
