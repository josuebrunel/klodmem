# klodmem

[![CI](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml/badge.svg)](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml)

Claude forgot what you two solved last week? klodmem gives Claude Code full-text search over its auto-memory and your raw conversation history, across every project. Local-first, one Go binary, no cloud.

> **Cross-project, cross-agent.** Register klodmem once and any MCP-capable agent gets Claude's memory and raw conversation history, from **every** project, not just the one it's working in.

## Quick start

**1. Install** (no Go needed). Swap the suffix for your platform: `linux-amd64`, `linux-arm64`, `darwin-amd64`, or `darwin-arm64`.

```sh
curl -L -o klodmem https://github.com/josuebrunel/klodmem/releases/latest/download/klodmem-darwin-arm64
chmod +x klodmem
sudo mv klodmem /usr/local/bin/
```

On Windows, grab `klodmem-windows-amd64.exe` from the [Releases page](https://github.com/josuebrunel/klodmem/releases) and put it on your `PATH`.

Have Go 1.25+? You can do this instead:

```sh
go install github.com/josuebrunel/klodmem/cmd/klodmem@latest
```

**2. Register it once**, so it works in every project:

```sh
claude mcp add --scope user klodmem -- klodmem
```

**3. Restart Claude Code and ask:**

> *"Have I debugged this `connection refused` error before?"*

That's it. No server, no API key, no config. To check the connection, run `claude mcp get klodmem`.

### Other agents

Any MCP-capable agent can use klodmem. Add it as a stdio server with the command `klodmem`, and it gets the same two tools.

**Hermes**

```sh
hermes mcp add klodmem --command klodmem
```

**Opencode**, in `opencode.jsonc`:

```jsonc
{
  "mcp": {
    "klodmem": { "type": "local", "command": ["klodmem"] }
  }
}
```

## What you get

Claude Code can only find its memories through one-line entries in `MEMORY.md`, matched by exact keyword, and each project's memories are invisible from every other project. klodmem fixes that:

- **Cross-project.** One index covers every project under `~/.claude/projects`, so what you solved in one repo is findable from any other.
- **Cross-agent.** It's a standard MCP server. Once an agent has added it, that agent can read Claude's memory and raw history through the tools below.
- **Searches full memory content.** Exposed as the `search_memory` tool.
- **Searches raw conversation history too.** Past discussions that never made it into memory are findable via `search_history`.
- **Live.** File watchers keep new memories and conversation turns searchable while a session is open.
- **Local and private.** No network calls. klodmem only reads your files.
- **Safe to try.** Your markdown stays the source of truth and the index is disposable. `klodmem -reset` rebuilds it any time.

Try these in any project:

| You ask | Claude does |
|---|---|
| *"Have I debugged this `connection refused` error before?"* | Calls `search_history`, finds the fix from a different project three weeks ago |
| *"Search my memories for docker-compose port conflicts."* | Calls `search_memory`, finds the note even if it's filed under "compose mapping" |

## Install options

<details>
<summary><b>Verify the download, macOS Gatekeeper</b></summary>

Each release ships a `checksums.txt` (SHA-256). Verify with:

```sh
sha256sum -c --ignore-missing checksums.txt
```

If Gatekeeper blocks the unsigned binary on macOS:

```sh
xattr -d com.apple.quarantine /usr/local/bin/klodmem
```

</details>

<details>
<summary><b>Build from source</b></summary>

```sh
git clone https://github.com/josuebrunel/klodmem.git
cd klodmem
make build   # produces ./bin/klodmem
```

Then point MCP at the binary: `claude mcp add --scope user klodmem -- /path/to/klodmem/bin/klodmem`

</details>

## How it works

- On startup klodmem scans `~/.claude/projects/*/memory/*.md` (skipping each `MEMORY.md` index) and every `~/.claude/projects/*/<session-id>.jsonl` transcript, then indexes them into SQLite FTS5.
- Transcripts are read incrementally: only newly appended lines on each scan. Both keep being watched while the MCP session is open.
- The index lives at `~/.claude/klodmem/klodmem.db` and is disposable. klodmem rebuilds it from your files.

## CLI reference

Running `klodmem` with no flags starts the MCP server, which is the normal way to use it. The flags below run one-off jobs instead:

| Command                   | Does what                                                      |
|---------------------------|----------------------------------------------------------------|
| `klodmem`                 | Start the MCP server (scan then watch, with live search)       |
| `klodmem -ingest`         | Index memory and history once, then exit                       |
| `klodmem -ingest.memory`  | Index memory files only, then exit                             |
| `klodmem -ingest.history` | Index conversation history only, then exit                     |
| `klodmem -reset`          | Truncate the index and re-index memory and history, then exit  |
| `klodmem -reset.memory`   | Truncate the memory index and re-index it, then exit           |
| `klodmem -reset.history`  | Truncate the history index and re-index it, then exit          |
| `klodmem -stat`           | Print index statistics and exit, no scanning                   |
| `klodmem -version`        | Print the klodmem version and exit                             |

**`-ingest`** is handy for warming the index right after installing, or running from cron independent of any Claude Code session.

**`-reset`** starts the index over and never touches your files. Reach for it when the index looks wrong, an older version left it messy, or the database has grown past what your transcripts justify.

- It clears rows instead of deleting the database file, so it's safe while other klodmem processes have the database open. They re-read from the start on their next scan.
- `-reset` equals `-reset.memory -reset.history`, and both imply the matching `-ingest` flags.
- Deleting the file by hand also works, but only while no MCP client is using it.

**`-stat`** prints what's currently indexed. Combine it with another flag to act then report, e.g. `klodmem -reset -stat` or `klodmem -ingest -stat`.

<details>
<summary>Example <code>-stat</code> output</summary>

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

</details>

## Configuration

All settings are optional environment variables:

| Variable              | Default                        | Meaning                                    |
|-----------------------|--------------------------------|--------------------------------------------|
| `KLODMEM_MEMORY_ROOT` | `~/.claude/projects`           | Root directory to scan for `*/memory/*.md` |
| `KLODMEM_DB_PATH`     | `~/.claude/klodmem/klodmem.db` | SQLite index file location                 |
| `KLODMEM_LOG_LEVEL`   | `info`                         | `debug`, `info`, `warn`, or `error`        |

Pass them via `-e` when registering:

```sh
claude mcp add --scope user klodmem -e KLODMEM_LOG_LEVEL=debug -- klodmem
```

## MCP tools

### `search_memory`

Full-text search over every project's memory files: the things Claude decided were worth writing down.

| Field     | Type   | Required | Description                                               |
|-----------|--------|----------|-----------------------------------------------------------|
| `query`   | string | yes      | Search terms                                              |
| `project` | string | no       | Restrict to one project's memory directory (slug form)    |
| `type`    | string | no       | Restrict to `user`, `feedback`, `project`, or `reference` |
| `limit`   | int    | no       | Max results (default 10)                                  |

Each result includes the project, type, name, description, a snippet of the match, and the file path, so Claude can read the full memory.

### `search_history`

Full-text search over raw session transcripts across every project: the actual debugging and back-and-forth that never got distilled into a memory file.

| Field     | Type   | Required | Description                         |
|-----------|--------|----------|-------------------------------------|
| `query`   | string | yes      | Search terms                        |
| `project` | string | no       | Restrict to one project (slug form) |
| `role`    | string | no       | Restrict to `user` or `assistant`   |
| `limit`   | int    | no       | Max results (default 10)            |

Each result includes the project, role, timestamp, a snippet of the match, the session ID, and the transcript file path.

Good to know:

- **Rawer content.** Only the authored text of user and assistant turns is extracted, never tool input/output, file contents, thinking blocks, or images.
- **A bigger index.** Transcripts are much larger than memory files. The first scan has a one-time cost (a few seconds per few hundred MB), then it's incremental.
- **No subagent transcripts.** Only each session's own top-level transcript is indexed.

## Contributing

Issues and pull requests are welcome. Run `make test` and `make lint` before opening a PR. If something doesn't index or search the way you expect, include the output of `klodmem -stat` and `KLODMEM_LOG_LEVEL=debug` in your issue.

```sh
make build   # compile ./bin/klodmem
make run     # build and run
make test    # run the test suite
make lint    # go vet + golangci-lint
make tidy    # go mod tidy
```
