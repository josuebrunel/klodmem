# klodmem

[![CI](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml/badge.svg)](https://github.com/josuebrunel/klodmem/actions/workflows/ci.yml)

Claude forgot what you two solved last week? klodmem gives Claude Code full-text search over its auto-memory, your raw conversation history, the prompts you typed, and your Plan Mode plans, across every project. Local-first, one Go binary, no cloud.

> **Cross-project, cross-agent.** Register klodmem once and any MCP-capable agent gets Claude's memory, conversation history, prompts, and plans, from **every** project, not just the one it's working in.

## Contents

- [Quick start](#quick-start)
  - [Install](#install)
  - [Register klodmem](#register-klodmem)
  - [Try it](#try-it)
  - [Other agents](#other-agents)
- [What you get](#what-you-get)
- [Install options](#install-options)
- [How it works](#how-it-works)
- [CLI reference](#cli-reference)
- [Configuration](#configuration)
- [MCP tools](#mcp-tools)
  - [`search_memory`](#search_memory)
  - [`search_history`](#search_history)
  - [`search_prompts`](#search_prompts)
  - [`search_plans`](#search_plans)
  - [`usage_stats`](#usage_stats)
- [Contributing](#contributing)

## Quick start

### Install

No Go needed. Swap the suffix for your platform: `linux-amd64`, `linux-arm64`, `darwin-amd64`, or `darwin-arm64`.

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

### Register klodmem

Register it once, so it works in every project:

```sh
claude mcp add --scope user klodmem -- klodmem
```

### Try it

Restart Claude Code and ask:

> *"Have I debugged this `connection refused` error before?"*

That's it. No server, no API key, no config. To check the connection, run `claude mcp get klodmem`.

### Other agents

Any MCP-capable agent can use klodmem. Add it as a stdio server with the command `klodmem`, and it gets the same tools.

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

Claude Code can only find its memories through one-line entries in `MEMORY.md`, matched by exact keyword, and each project's memories are invisible from every other project. Prompts and plans are global but only surface in the picker or in the moment. klodmem fixes that.

Four sources, four tools:

| Source | Tool | Finds |
|--------|------|-------|
| Auto-memory (`~/.claude/projects/*/memory/*.md`) | `search_memory` | Notes Claude decided to save |
| Session transcripts (`*/<session-id>.jsonl`) | `search_history` | Past debugging and back-and-forth |
| Prompt history (`~/.claude/history.jsonl`) | `search_prompts` | What you asked, with time and session |
| Plan Mode plans (`~/.claude/plans/*.md`) | `search_plans` | Planning documents |

Why it's useful:

- **Cross-project.** One index covers every project under `~/.claude/projects`, so what you solved in one repo is findable from any other.
- **Cross-agent.** It's a standard MCP server. Once an agent has added it, that agent can read Claude's memory, conversation history, prompts, and plans through the tools below.
- **Live.** File watchers keep new memories, conversation turns, prompts, and plans searchable while a session is open.
- **Knows how it's used.** Every tool call is logged locally; `usage_stats` and `klodmem -stat` show calls per tool, errors, latency, and top queries.
- **Local and private.** No network calls. klodmem only reads your files.
- **Safe to try.** Your markdown stays the source of truth and the index is disposable. `klodmem -reset` rebuilds it any time.

Try these in any project:

| You ask | Claude does |
|---|---|
| *"Have I debugged this `connection refused` error before?"* | Calls `search_history`, finds the fix from a different project three weeks ago |
| *"Search my memories for docker-compose port conflicts."* | Calls `search_memory`, finds the note even if it's filed under "compose mapping" |
| *"Which time did I ask about migrating the billing schema?"* | Calls `search_prompts`, finds the prompt and the session to resume |
| *"What was the plan for the auth refactor?"* | Calls `search_plans`, finds the Plan Mode document |

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

- On startup klodmem scans `~/.claude/projects/*/memory/*.md` (skipping each `MEMORY.md` index), every `~/.claude/projects/*/<session-id>.jsonl` transcript, the global prompt history at `~/.claude/history.jsonl`, and every `~/.claude/plans/*.md`, then indexes them into SQLite FTS5.
- Transcripts and the prompt history are read incrementally: only newly appended lines on each scan. All four sources keep being watched while the MCP session is open.
- Each MCP tool call is appended to a local `tool_calls` table (tool, query, project, result count, error flag, duration). `usage_stats` and `-stat` aggregate it.
- The index lives at `~/.claude/klodmem/klodmem.db` and is disposable. klodmem rebuilds it from your files. Usage data is not rebuildable, so resets keep it.

## CLI reference

Running `klodmem` with no flags starts the MCP server, which is the normal way to use it. The flags below run one-off jobs instead:

| Command                   | Does what                                                      |
|---------------------------|----------------------------------------------------------------|
| `klodmem`                 | Start the MCP server (scan then watch, with live search)       |
| `klodmem -ingest`         | Index every source once, then exit                             |
| `klodmem -ingest.memory`  | Index memory files only, then exit                             |
| `klodmem -ingest.history` | Index conversation history only, then exit                     |
| `klodmem -ingest.prompts` | Index the global prompt history only, then exit                |
| `klodmem -ingest.plans`   | Index Plan Mode plans only, then exit                          |
| `klodmem -reset`          | Truncate the index and re-index every source, then exit        |
| `klodmem -reset.memory`   | Truncate the memory index and re-index it, then exit           |
| `klodmem -reset.history`  | Truncate the history index and re-index it, then exit          |
| `klodmem -reset.prompts`  | Truncate the prompt history index and re-index it, then exit   |
| `klodmem -reset.plans`    | Truncate the plan index and re-index it, then exit             |
| `klodmem -stat`           | Print index and MCP usage statistics and exit, no scanning     |
| `klodmem -version`        | Print the klodmem version and exit                             |

**`-ingest`** is handy for warming the index right after installing, or running from cron independent of any Claude Code session.

**`-reset`** starts the index over and never touches your files. Reach for it when the index looks wrong, an older version left it messy, or the database has grown past what your files justify.

- It clears rows instead of deleting the database file, so it's safe while other klodmem processes have the database open. They re-read from the start on their next scan.
- `-reset` equals `-reset.memory -reset.history -reset.prompts -reset.plans`, and all imply the matching `-ingest` flags.
- Resets leave MCP usage data (`tool_calls`) intact, since it cannot be rebuilt from files.
- Deleting the file by hand also works, but only while no MCP client is using it.

**`-stat`** prints what's currently indexed plus how the MCP tools have been used. Combine it with another flag to act then report, e.g. `klodmem -reset -stat` or `klodmem -ingest -stat`.

<details>
<summary>Example <code>-stat</code> output</summary>

```
klodmem index stats
  db: /home/user/.claude/klodmem/klodmem.db (5.9 MiB)

memory
  files: 1

plans
  files: 24

prompts
  prompts: 2654
  sessions: 312
  projects: 22

history
  messages: 6398 (assistant: 5402, user: 996)
  sessions: 104
  projects: 22
  range: 2026-08-04T05:03:49.522Z to 2026-09-03T11:18:58.758Z

by project
  -home-user-workspace-project-alpha     2436 messages    24 sessions
  -home-user-workspace-project-beta       894 messages    13 sessions
  ...

mcp calls
  calls: 42 (errors: 3)
  range: 2026-09-01T09:12:00Z to 2026-09-03T11:18:58Z
  search_history       20 calls     2 errors     12 ms avg  last 2026-09-03T11:18:58Z
  search_memory        12 calls     0 errors      8 ms avg  last 2026-09-03T10:02:11Z
  usage_stats           6 calls     0 errors      1 ms avg  last 2026-09-03T11:18:58Z
  search_prompts        4 calls     1 error       9 ms avg  last 2026-09-02T16:40:00Z

  top queries
        5  connection refused
        3  docker-compose ports
```

</details>

## Configuration

All settings are optional environment variables:

| Variable               | Default                        | Meaning                                    |
|------------------------|--------------------------------|--------------------------------------------|
| `KLODMEM_MEMORY_ROOT`  | `~/.claude/projects`           | Root directory to scan for `*/memory/*.md` and session transcripts |
| `KLODMEM_PLANS_ROOT`   | `~/.claude/plans`              | Directory to scan for Plan Mode `*.md`     |
| `KLODMEM_HISTORY_PATH` | `~/.claude/history.jsonl`      | Global prompt history file                 |
| `KLODMEM_DB_PATH`      | `~/.claude/klodmem/klodmem.db` | SQLite index file location                 |
| `KLODMEM_LOG_LEVEL`    | `info`                         | `debug`, `info`, `warn`, or `error`        |

Pass them via `-e` when registering:

```sh
claude mcp add --scope user klodmem -e KLODMEM_LOG_LEVEL=debug -- klodmem
```

## MCP tools

klodmem exposes four search tools, one per source, plus `usage_stats`. All are local; the searches are full-text and cross-project (plans are global, so they have no project filter).

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

### `search_prompts`

Full-text search over Claude Code's global prompt history (`~/.claude/history.jsonl`): the prompts you typed, across every project, including pasted content. Unlike `search_history`, this is not limited to sessions whose transcript still exists.

| Field     | Type   | Required | Description                         |
|-----------|--------|----------|-------------------------------------|
| `query`   | string | yes      | Search terms                        |
| `project` | string | no       | Restrict to one project (slug form) |
| `limit`   | int    | no       | Max results (default 10)            |

Each result includes the project, timestamp, a snippet, the session ID, and the history file path.

### `search_plans`

Full-text search over Claude Code's Plan Mode plans (`~/.claude/plans/*.md`). Plans are global, so there is no project filter.

| Field   | Type   | Required | Description              |
|---------|--------|----------|--------------------------|
| `query` | string | yes      | Search terms             |
| `limit` | int    | no       | Max results (default 10) |

Each result includes the plan's title, description, a snippet, and the file path.

### `usage_stats`

Aggregate statistics for klodmem's own MCP usage, recorded locally on every call.

| Field   | Type | Required | Description                              |
|---------|------|----------|------------------------------------------|
| `limit` | int  | no       | Max top queries to return (default 10)   |

Returns total calls, errors, the time range, per-tool calls/errors/average latency/last use, and the most frequent queries.

Good to know:

- **Every call is recorded.** That includes validation failures (`query must not be empty`) and `usage_stats` itself, so the numbers reflect real traffic.
- **Local only.** Query text is stored in the local `tool_calls` table to power "top queries"; it never leaves your machine.
- **Survives resets.** Usage is not rebuildable from files, so `-reset` keeps it.

## Contributing

Issues and pull requests are welcome. Run `make test` and `make lint` before opening a PR. If something doesn't index or search the way you expect, include the output of `klodmem -stat` and `KLODMEM_LOG_LEVEL=debug` in your issue.

```sh
make build   # compile ./bin/klodmem
make run     # build and run
make test    # run the test suite
make lint    # go vet + golangci-lint
make tidy    # go mod tidy
```
