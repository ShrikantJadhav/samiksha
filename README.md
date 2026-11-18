# Viveka

**विवेक** — *discernment*: the capacity to distinguish a genuinely sound change from one that only appears to pass.

`viveka` is a reviewer agent for code changes. Given a diff, it runs an agent loop — **model → tool calls → observations → verdict** — to gather evidence and decide whether the change is sound or merely *looks* green because its guardrails were weakened.

It is designed for the failure mode where "tests pass" is a claim, not proof: skipped tests, removed assertions, silenced linters, relaxed CI, untested new logic, or hidden capabilities.

## How it works

Viveka is agentic: it chooses its next step from what it observes.

1. Reads the diff under review and runs [`blast`](../blast) (deterministic diff forensics) as an instrument panel.
2. Forms hypotheses about what the change claims to do and which signals matter.
3. Investigates using tools — reading files, searching code, and (optionally) running commands/tests — following up on suspicious findings rather than guessing.
4. Emits an evidence-backed verdict: `SOUND`, `CONCERNS`, or `UNSOUND`, with cited observations and a recommendation. It never auto-merges.

`blast` provides the hard, deterministic floor; viveka does the reasoning above it, spending model calls only where evidence needs interpretation.

## Status

**v0 — investigate & verdict.** It can inspect a diff, consult blast, read/search the repo, and run existing commands/tests. It does **not** yet generate probe tests or run changes in a sandbox (planned).

## Build

```sh
go build -o viveka ./cmd/viveka
# or: make build
```

Requires Go 1.23+. No external Go dependencies.

## Requirements

- An OpenAI-compatible chat completions API (OpenAI, many hosted providers, or a local server such as Ollama / LM Studio / vLLM exposing a compatible endpoint).
- [`blast`](../blast) on your `PATH` (or pass `--blast /path/to/blast`) for deterministic diff findings. Viveka can still review without it, but with less signal.

## Usage

```sh
# review the current branch against its base (like a PR)
export VIVEKA_MODEL="gpt-4o-mini"        # or any compatible model id
export OPENAI_API_KEY="..."               # or VIVEKA_API_KEY
./viveka review

# common forms
./viveka review --base main
./viveka review --staged
./viveka review --patch change.patch
./viveka review --intent "fix flaky session lookup"

# allow the agent to run tests/commands when investigating
./viveka review --allow-run
```

If `blast` lives elsewhere (for example the sibling repo in this workspace):

```sh
./viveka review --blast ../blast/blast
```

### Flags

| Flag | Meaning |
| --- | --- |
| `--base <ref>` | diff `<ref>...HEAD` (default: `origin/HEAD`, `main`, `master`, or `HEAD~1`) |
| `--staged` | diff the index against `HEAD` |
| `--patch <file>` | read a unified diff from a file, or `-` for stdin |
| `--dir <path>` | working directory / repository root (default: cwd) |
| `--intent <text>` | what the change claims to do (PR title/description) |
| `--model <id>` | model name (env: `VIVEKA_MODEL`, `OPENAI_MODEL`) |
| `--base-url <url>` | API base URL (env: `VIVEKA_BASE_URL`, `OPENAI_BASE_URL`; default `https://api.openai.com/v1`) |
| `--api-key <key>` | API key (env: `VIVEKA_API_KEY`, `OPENAI_API_KEY`) |
| `--blast <path>` | path to `blast` binary (env: `VIVEKA_BLAST`; default `blast`) |
| `--allow-run` | let the agent execute shell commands (e.g. run tests) |
| `--run-timeout <d>` | per-command timeout when `--allow-run` is set (default `60s`) |
| `--max-steps <n>` | maximum agent tool rounds (default `12`) |
| `-q, --quiet` | suppress progress messages (verdict still printed to stdout) |

Progress is written to stderr; the verdict is written to stdout. Exit `0` when a verdict is produced, `2` on error.

## Tools available to the agent

| Tool | Purpose |
| --- | --- |
| `get_diff` | read the full unified diff under review |
| `blast_scan` | run `blast scan --json` for deterministic findings and risk score |
| `read_file` | read source/test files with line numbers |
| `list_dir` | list directory entries |
| `search` | regex search over files (`rg` if available, else `grep`) |
| `run_command` | execute a shell command (only when `--allow-run` is set) |

## Safety notes

- `--allow-run` executes commands the model chooses in your working directory, with your permissions. Leave it off to review from static evidence only; enable it when you want the agent to run tests and you trust the environment.
- Verdicts are model-generated reasoning over tool observations. Treat them as a review aid, not an authority — especially at low confidence or when verification was not possible.

## Layout

```
cmd/viveka        CLI, flags, wiring
internal/agent    agent loop and system prompt
internal/llm      chat client interface and OpenAI-compatible implementation
internal/tools    tool registry (diff, blast, fs, search, run)
internal/diffsource  diff acquisition (git base/staged/patch/stdin)
```

## Roadmap

1. **Investigate & verdict** (v0) — evidence gathering over the diff with deterministic + model reasoning.
2. **Evidence runs** — compare base vs head test results and patch coverage (which added lines were exercised).
3. **Probe generation** — author and run targeted tests in a sandbox to validate what the change claims.
```