#!/usr/bin/env python3
"""Run Samiksha against the labelled diffs in testdata/cases and score it.

No third-party dependencies. Raw stdout/stderr for each run is saved under
testdata/eval-out/ so a surprising verdict can be inspected in full.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CASES_DIR = ROOT / "testdata" / "cases"
OUT_DIR = ROOT / "testdata" / "eval-out"
VERDICT_RE = re.compile(r"^## Verdict:\s*(SOUND|CONCERNS|UNSOUND)\s*$", re.MULTILINE)


def parse_args():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", default=str(ROOT / "samiksha"))
    p.add_argument("--manifest", default=str(CASES_DIR / "manifest.json"))
    p.add_argument("--blast", default=str(ROOT.parent / "blast" / "blast"))
    p.add_argument("--model", default=os.getenv("SAMIKSHA_MODEL", "qwen2.5:7b"))
    p.add_argument("--base-url", default=os.getenv("SAMIKSHA_BASE_URL", "http://localhost:11434/v1"))
    p.add_argument("--api-key", default=os.getenv("SAMIKSHA_API_KEY", "ollama"))
    p.add_argument("--mode", choices=["structured", "single", "both"], default="structured")
    p.add_argument("--cases", help="comma-separated case names to run (default: all)")
    p.add_argument("--timeout", type=int, default=900, help="seconds per run")
    p.add_argument("--strict", action="store_true", help="exit 1 if any verdict is wrong")
    return p.parse_args()


def load_cases(path, only):
    with open(path) as f:
        cases = json.load(f)["cases"]
    if only:
        wanted = set(only.split(","))
        cases = [c for c in cases if c["name"] in wanted]
    return cases


def run_case(case, mode, args):
    patch = CASES_DIR / case["patch"]
    cmd = [
        args.binary,
        "review",
        "--patch", str(patch),
        "--blast", str(Path(args.blast).resolve()),
        "--intent", case["intent"],
    ]
    if mode == "single":
        cmd.append("--single-agent")

    env = dict(os.environ)
    env.update({
        "SAMIKSHA_MODEL": args.model,
        "SAMIKSHA_BASE_URL": args.base_url,
        "SAMIKSHA_API_KEY": args.api_key,
    })
    for key in ("PRAMANA_MODEL", "PRAMANA_BASE_URL", "PRAMANA_API_KEY"):
        env.pop(key, None)

    started = time.time()
    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=args.timeout, env=env
        )
        stdout, stderr, rc = proc.stdout, proc.stderr, proc.returncode
    except subprocess.TimeoutExpired as e:
        stdout = e.stdout or ""
        stderr = (e.stderr or "") + "\n[eval: timed out]"
        rc = 124
    elapsed = time.time() - started

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    stem = f"{case['name']}.{mode}"
    (OUT_DIR / f"{stem}.out.txt").write_text(stdout)
    (OUT_DIR / f"{stem}.err.txt").write_text(stderr)

    match = VERDICT_RE.search(stdout)
    verdict = match.group(1) if match else "PARSE_ERROR"
    expected = case["expect_verdict"]
    markers = case.get("expect_evidence", [])
    hits = [m for m in markers if m.lower() in stdout.lower()]
    steps = len(re.findall(r"step \d+/\d+: thinking", stderr))
    tools = len(re.findall(r": calling ", stderr))
    floors = len(re.findall(r"Deterministic floor applied", stdout))
    ok = verdict == expected and len(hits) == len(markers)
    return {
        "name": case["name"],
        "mode": mode,
        "expected": expected,
        "verdict": verdict,
        "evidence": f"{len(hits)}/{len(markers)}",
        "ev_hits": len(hits),
        "ev_total": len(markers),
        "missed": [m for m in markers if m not in hits],
        "steps": steps,
        "tools": tools,
        "floors": floors,
        "secs": round(elapsed, 1),
        "ok": ok,
        "rc": rc,
    }


def print_table(rows):
    header = f"{'case':<18} {'mode':<10} {'expect':<9} {'got':<11} {'ev':<5} {'steps':>5} {'tools':>5} {'floor':>5} {'secs':>6}  result"
    print(header)
    print("-" * len(header))
    for r in rows:
        print(
            f"{r['name']:<18} {r['mode']:<10} {r['expected']:<9} {r['verdict']:<11} "
            f"{r['evidence']:<5} {r['steps']:>5} {r['tools']:>5} {r['floors']:>5} "
            f"{r['secs']:>6}  {'PASS' if r['ok'] else 'FAIL'}"
        )
        if r["missed"]:
            print(f"{'':<18} missed evidence: {', '.join(r['missed'])}")
        if r["rc"] not in (0,):
            print(f"{'':<18} exit code {r['rc']}")


def print_summary(rows):
    print()
    for mode in sorted({r["mode"] for r in rows}):
        subset = [r for r in rows if r["mode"] == mode]
        verdicts = sum(r["verdict"] == r["expected"] for r in subset)
        ev_hits = sum(r["ev_hits"] for r in subset)
        ev_total = sum(r["ev_total"] for r in subset)
        passed = sum(r["ok"] for r in subset)
        print(
            f"{mode}: {passed}/{len(subset)} fully correct, "
            f"{verdicts}/{len(subset)} verdicts correct, "
            f"evidence {ev_hits}/{ev_total}, "
            f"{sum(r['secs'] for r in subset):.1f}s total"
        )


def main():
    args = parse_args()
    cases = load_cases(args.manifest, args.cases)
    if not cases:
        print("no matching cases", file=sys.stderr)
        return 2
    modes = ["structured", "single"] if args.mode == "both" else [args.mode]
    rows = []
    for case in cases:
        for mode in modes:
            result = run_case(case, mode, args)
            rows.append(result)
            print(
                f"ran {case['name']} ({mode}): {result['verdict']} "
                f"in {result['secs']}s", file=sys.stderr
            )
    print()
    print_table(rows)
    print_summary(rows)
    print(f"\nraw output: {OUT_DIR}")
    if args.strict and any(not r["ok"] for r in rows):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
