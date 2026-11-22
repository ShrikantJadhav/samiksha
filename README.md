# Samiksha

**समीक्षा** — *review, critical examination*: looking closely at a change and judging it on what can be shown, not guesswork.

`samiksha` is a reviewer agent for code changes. Given a diff, it combines a deterministic [`blast`](../blast) scan with narrow, evidence-backed review passes and a consolidation step, to establish whether the change is genuinely sound or merely *looks* green because its guardrails were weakened.

It is designed for the failure mode where "tests pass" is a claim, not proof: skipped tests, removed assertions, silenced linters, relaxed CI, untested new logic, or hidden capabilities.

## How it works

Samiksha is agentic within each pass — it can read files, search code, and (optionally) run commands/tests — but the overall review is structured rather than one long free-form loop:

1. **Blast floor:** runs [`blast`](../blast) (deterministic diff forensics) itself. Its findings are facts that no model pass may dismiss.
2. **Narrow passes:** three specialist questions run separately — intent vs actual diff, weakened checks, and new capabilities/data exposure. Each reports `Concerns` **and** `Ruled out` items with quoted evidence. Asking one simple question per pass avoids the "find one bug and stop" failure mode of small models.
3. **Triage:** audits the dismissals. A dismissal is only accepted with concrete evidence; shaky self-dismissals are promoted back to concerns.
4. **Consolidation:** one final call produces `SOUND`, `CONCERNS`, or `UNSOUND` with quoted evidence, findings, ruled-out items, a recommendation, and calibrated confidence. It never auto-merges.

`--single-agent` keeps the original one-loop reviewer for A/B comparisons. `--show-passes` prints each intermediate report to stderr for debugging.

## Status

**v0 — investigate & verdict.** It can inspect a diff, consult blast, read/search the repo, and run existing commands/tests. It does **not** yet generate probe tests or run changes in a sandbox (planned).

## Build

```sh
go build -o samiksha ./cmd/samiksha
# or: make build
```

Requires Go 1.23+. No external Go dependencies.

## Requirements

- An OpenAI-compatible chat completions API (OpenAI, many hosted providers, or a local server such as Ollama / LM Studio / vLLM exposing a compatible endpoint).
- [`blast`](../blast) on your `PATH` (or pass `--blast /path/to/blast`) for deterministic diff findings. Samiksha can still review without it, but with less signal.

## Usage

```sh
# review the current branch against its base (like a PR)
export SAMIKSHA_MODEL="gpt-4o-mini"        # or any compatible model id
export OPENAI_API_KEY="..."               # or SAMIKSHA_API_KEY
./samiksha review

# common forms
./samiksha review --base main
./samiksha review --staged
./samiksha review --patch change.patch
./samiksha review --intent "fix flaky session lookup"

# allow the agent to run tests/commands when investigating
./samiksha review --allow-run
```

If `blast` lives elsewhere (for example the sibling repo in this workspace):

```sh
./samiksha review --blast ../blast/blast
```

### Flags

| Flag | Meaning |
| --- | --- |
| `--base <ref>` | diff `<ref>...HEAD` (default: `origin/HEAD`, `main`, `master`, or `HEAD~1`) |
| `--staged` | diff the index against `HEAD` |
| `--patch <file>` | read a unified diff from a file, or `-` for stdin |
| `--dir <path>` | working directory / repository root (default: cwd) |
| `--intent <text>` | what the change claims to do (PR title/description) |
| `--model <id>` | model name (env: `SAMIKSHA_MODEL`, `OPENAI_MODEL`) |
| `--base-url <url>` | API base URL (env: `SAMIKSHA_BASE_URL`, `OPENAI_BASE_URL`; default `https://api.openai.com/v1`) |
| `--api-key <key>` | API key (env: `SAMIKSHA_API_KEY`, `OPENAI_API_KEY`) |
| `--blast <path>` | path to `blast` binary (env: `SAMIKSHA_BLAST`; default `blast`) |
| `--allow-run` | let the agent execute shell commands (e.g. run tests) |
| `--run-timeout <d>` | per-command timeout when `--allow-run` is set (default `60s`) |
| `--pass-steps <n>` | max tool rounds per specialist pass (default `3`) |
| `--single-agent` | use the original single-loop reviewer instead of specialist passes |
| `--show-passes` | print intermediate pass/triage reports to stderr |
| `--max-steps <n>` | max steps for `--single-agent` mode (default `12`) |
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
cmd/samiksha         CLI, flags, wiring
internal/agent      structured pipeline: passes, triage, consolidation, single-agent loop
internal/llm      chat client interface and OpenAI-compatible implementation
internal/tools    tool registry (diff, blast, fs, search, run)
internal/diffsource  diff acquisition (git base/staged/patch/stdin)
```

## Roadmap

1. **Investigate & verdict** (v0) — evidence gathering over the diff with deterministic + model reasoning.
2. **Evidence runs** — compare base vs head test results and patch coverage (which added lines were exercised).
3. **Probe generation** — author and run targeted tests in a sandbox to validate what the change claims.
```