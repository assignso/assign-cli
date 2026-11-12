"""Check the compiled Bun candidate offline, with optional Go parity."""

import argparse
import json
import os
from pathlib import Path
from queue import Empty, Queue
import shlex
import subprocess
import sys
import tempfile
from threading import Thread


HERE = Path(__file__).resolve().parent
CLI = HERE.parents[1]
NETWORK_PROBE = """import errno, socket
for family, endpoint in [(socket.AF_INET, ('127.0.0.1', 9)), (socket.AF_INET6, ('::1', 9))]:
    with socket.socket(family) as stream:
        stream.settimeout(1)
        assert stream.connect_ex(endpoint) in (errno.EACCES, errno.EPERM)
"""


def make_go_probe(work):
    destination = CLI / "internal/cli/rewrite_fixture_probe_test.go"
    overlay = work / "go-overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(destination): str(HERE / "go-probe.go.txt")}}))
    binary = work / ("go-probe.exe" if sys.platform == "win32" else "go-probe")
    env = {**os.environ, "GOPROXY": "off", "GOTOOLCHAIN": "go1.26.10", "CGO_ENABLED": "0",
           "GOCACHE": str(work / "go-cache")}
    subprocess.run(["go", "test", "-c", "-overlay", str(overlay), "-o", str(binary), "./internal/cli"],
                   cwd=CLI, env=env, check=True, timeout=120)
    return binary


def fixture_environment(paths, home):
    env = {"PATH": os.defpath, "HOME": str(home), "USERPROFILE": str(home),
           "XDG_CONFIG_HOME": str(home), "APPDATA": str(home), "LOCALAPPDATA": str(home),
           "ASSIGN_FIXTURE_CASE": str(paths[0]), "ASSIGN_FIXTURE_TRACE": str(paths[1])}
    for key in ("SystemRoot", "WINDIR"):
        if key in os.environ:
            env[key] = os.environ[key]
    for extra in ("TEMP", "TMP", "TMPDIR"):
        env[extra] = str(home)
    return env


def run_case(prefix, binary, fixture, case, paths, home):
    env = fixture_environment(paths, home)
    args = [str(binary), "-test.run=^TestCLIRewriteFixture$"] if fixture == "go" else [str(binary), *case["argv"]]
    paths[1].unlink(missing_ok=True)
    process = subprocess.run([*prefix, *args], input=case.get("stdin", ""), capture_output=True,
                             text=True, env=env, cwd=home, timeout=12)
    trace = json.loads(paths[1].read_text()) if paths[1].exists() else None
    return {"argv": case["argv"], "stdin": case.get("stdin", ""), "stdout": process.stdout,
            "stderr": process.stderr, "exit": process.returncode, "requests": trace}


def check_child_termination(prefix, candidate, paths, home):
    argv = ["__fixture", "wait"]
    paths[0].write_text(json.dumps({"name": "fixture-child-termination", "argv": argv, "requests": []}))
    ready = Queue(maxsize=1)
    with subprocess.Popen([*prefix, str(candidate), *argv], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, text=True, cwd=home,
                          env=fixture_environment(paths, home)) as child:
        reader = Thread(target=lambda: ready.put(child.stdout.readline()), daemon=True)
        reader.start()
        try:
            if ready.get(timeout=5) != "ready\n" or child.poll() is not None:
                raise AssertionError("fixture child did not remain active after signaling readiness")
            child.terminate()
            if child.wait(timeout=5) == 0:
                raise AssertionError("fixture child completed before termination")
        except Empty as error:
            raise AssertionError("fixture child did not signal readiness") from error
        finally:
            if child.poll() is None:
                child.kill()
                child.wait(timeout=5)
            reader.join(timeout=5)
    return child.returncode


def check(case, result):
    fields = ["exit"] if case["exit"] == 70 else ["exit", "stdout", "stderr"]
    for key in fields:
        assert result[key] == case[key], f"{key} expected {case[key]!r}, got {result[key]!r}"
    if case["exit"] != 70:
        expected = [{k: req[k] for k in ("method", "path")} for req in case["requests"]]
        assert result["requests"] == expected, "request trace differs"
    else:
        assert result["requests"] is not None, "fixture did not report its trace"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--scratch", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    baseline = parser.add_mutually_exclusive_group()
    baseline.add_argument("--go-probe", type=Path)
    baseline.add_argument("--candidate-only", action="store_true", help="Check Bun without building or running Go.")
    parser.add_argument("--deny-network", help="Native wrapper command. Required outside macOS.")
    options = parser.parse_args()
    if options.deny_network:
        prefix = shlex.split(options.deny_network)
    elif sys.platform == "darwin":
        prefix = ["/usr/bin/sandbox-exec", "-p", "(version 1) (allow default) (deny network*)"]
    else:
        parser.error("a native deny-network wrapper is required")

    subprocess.run([*prefix, sys.executable, "-c", NETWORK_PROBE], check=True, timeout=5,
                   env={"PATH": os.defpath})
    options.scratch.mkdir(parents=True, exist_ok=True)
    cases = json.loads((HERE / "cases.json").read_text())
    records = []
    with tempfile.TemporaryDirectory(dir=options.scratch) as directory:
        work = Path(directory).resolve()
        candidate = options.candidate.resolve(strict=True)
        binaries = [("bun", candidate)]
        if not options.candidate_only:
            go_probe = options.go_probe.resolve(strict=True) if options.go_probe else make_go_probe(work)
            binaries.insert(0, ("go", go_probe))
        home = work / "home"
        home.mkdir(mode=0o700)
        paths = (work / "case.json", work / "trace.json")
        for case in cases:
            paths[0].write_text(json.dumps(case))
            for name, binary in binaries:
                if case.get("engine") and case["engine"] != name:
                    continue
                result = run_case(prefix, binary, name, case, paths, home)
                row = {"case": case["name"], "engine": name, **result}
                try:
                    check(case, result)
                except AssertionError as error:
                    row["failure"] = str(error)
                records.append(row)
        lifecycle = {"case": "fixture-child-termination", "engine": "bun"}
        try:
            lifecycle["exit"] = check_child_termination(prefix, candidate, paths, home)
        except (AssertionError, subprocess.TimeoutExpired) as error:
            lifecycle["failure"] = str(error)
        records.append(lifecycle)
    receipt = {"mode": "candidate-only" if options.candidate_only else "go-parity",
               "network_denied": True, "cases": records}
    options.receipt.write_text(json.dumps(receipt, indent=2) + "\n")
    failures = [f'{r["case"]} {r["engine"]}: {r["failure"]}' for r in records if "failure" in r]
    for failure in failures:
        print(failure)
    print(f"{len(records) - len(failures)} passed, {len(failures)} failed")
    return bool(failures)


if __name__ == "__main__":
    sys.exit(main())
