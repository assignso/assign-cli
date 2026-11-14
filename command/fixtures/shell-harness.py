"""Deterministic shell parity for a supplied native executable (no remote actions)."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    candidate = args.candidate.resolve(strict=True)
    contract = json.loads((Path(__file__).parents[1] / 'src/shell-contract.json').read_text())
    cases = contract['cases'] + [dict(argv=['completion', shell], code=0, stdout=text, stderr='')
                                 for shell, text in contract['scripts'].items()]
    cases += [dict(argv=['version', 'quoted "title" \\ path 日本語'], code=2, stdout='',
                   stderr='assign: unknown argument "quoted \\"title\\" \\\\ path 日本語" for "assign version"\n')]
    with tempfile.TemporaryDirectory(prefix='assign-shell-') as home:
        env = dict(PATH=os.defpath, HOME=home, USERPROFILE=home, APPDATA=home,
                   LOCALAPPDATA=home, XDG_CONFIG_HOME=home,
                   ASSIGN_CONFIG_FILE=str(Path(home) / 'absent.json'))
        for key in ('SystemRoot', 'WINDIR'):
            if key in os.environ:
                env[key] = os.environ[key]
        passed = 0
        for case in cases:
            result = subprocess.run([str(candidate), *case['argv']], cwd=home, env=env,
                                    stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=45)
            actual = dict(code=result.returncode, stdout=result.stdout, stderr=result.stderr)
            expected = {key: case[key] for key in actual}
            if actual != expected:
                raise AssertionError(f"shell case {case['argv']!r}: {actual!r} != {expected!r}")
            passed += 1
    receipt = dict(schema=1, candidate_sha256=hashlib.sha256(candidate.read_bytes()).hexdigest(),
                   system=platform.system(), machine=platform.machine(), passed=passed, failed=0,
                   scope='native shell argv/streams/exits; no API or provider action; Windows execution requires Windows host')
    args.receipt.write_text(json.dumps(receipt, indent=2) + '\n')
    print(f'{passed} passed, 0 failed')


if __name__ == '__main__':
    main()
