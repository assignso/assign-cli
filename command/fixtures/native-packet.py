"""Run one explicitly supplied native feasibility job; never provision hosts."""
import argparse
import hashlib
import json
import platform
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile

from harness import fixture_environment, NETWORK_PROBE

HERE = Path(__file__).resolve().parent


def executable(value):
    path = Path(value).resolve(strict=True)
    if not path.is_file() or path.suffix.lower() in ('.cmd', '.bat'):
        raise ValueError('supply an approved native executable, not a shell shim')
    if sys.platform == 'win32' and path.suffix.lower() != '.exe':
        raise ValueError('Windows inputs must resolve to approved native .exe files')
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--job', required=True)
    parser.add_argument('--execution', choices=['native', 'emulated', 'unavailable'], required=True,
                        help='Operator attestation; cross-builds are not native evidence')
    parser.add_argument('--ordinary')
    parser.add_argument('--sdk-fixture')
    parser.add_argument('--vault-fixture')
    parser.add_argument('--sdk-identity', type=Path, help='sdk-identity.json from stage-sdk-fixture.py')
    parser.add_argument('--deny-network', help='JSON array of native wrapper argv')
    parser.add_argument('--scratch', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    jobs = json.loads((HERE / 'native-jobs.json').read_text())['jobs']
    job = next((j for j in jobs if j['id'] == args.job), None)
    if job is None:
        parser.error('unknown job')
    receipt = dict(job=args.job, execution=args.execution, system=platform.system(),
                   machine=platform.machine(), release=platform.release(), assertions=[],
                   vendor_sandbox_auth='unverified', native_qualification=False)
    try:
        if args.execution == 'unavailable':
            raise ValueError('host unavailable; packet did not run')
        if (platform.system(), platform.machine().lower()) != (job['system'], job['machine'].lower()):
            raise ValueError('host does not match job')
        binaries = {k: executable(getattr(args, k)) for k in ('ordinary', 'sdk_fixture', 'vault_fixture')}
        identity = json.loads(args.sdk_identity.read_text())
        if identity.get('sdk_revision') != 'd12065d63d9dd327b2e42f14ada155822f31702e':
            raise ValueError('SDK fixture revision differs from reviewed pin')
        receipt['sdk_identity'] = identity
        receipt['sha256'] = {k: hashlib.sha256(p.read_bytes()).hexdigest() for k, p in binaries.items()}
        if args.deny_network:
            prefix = json.loads(args.deny_network)
            if not isinstance(prefix, list) or not prefix or not all(isinstance(x, str) for x in prefix):
                raise ValueError('wrapper must be a nonempty argv array')
            prefix[0] = str(executable(prefix[0]))
        elif sys.platform == 'darwin':
            prefix = ['/usr/bin/sandbox-exec', '-p', '(version 1) (allow default) (deny network*)']
        else:
            raise ValueError('native deny-network wrapper required')
        args.scratch.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=args.scratch) as temp:
            home = Path(temp).resolve()
            env = fixture_environment((home / 'case.json', home / 'trace.json'), home)
            def run(argv):
                return subprocess.run([*prefix, *map(str, argv)], cwd=home, env=env,
                                      capture_output=True, text=True, timeout=120, check=True)
            run([sys.executable, '-c', NETWORK_PROBE])
            receipt['assertions'].append('IPv4 and IPv6 denied')
            for argv in (['version'], ['--help'], ['help', 'task', 'show'], ['completions', 'bash'], ['completion', 'zsh'], ['completion', 'fish']):
                result = run([binaries['ordinary'], *argv])
                if not result.stdout.strip() or result.stderr:
                    raise AssertionError('ordinary startup output missing or stderr nonempty')
                receipt['assertions'].append('ordinary ' + ' '.join(argv))
            # An argv array preserves spaces, quotes, backslashes and Unicode on native Windows.
            title = 'quoted "title" \\ path 日本語'
            quoted = subprocess.run([*prefix, str(binaries['ordinary']), 'version', title],
                                    cwd=home, env=env, capture_output=True, text=True, timeout=15)
            expected = 'assign: unknown argument ' + json.dumps(title, ensure_ascii=False) + ' for "assign version"\n'
            if quoted.returncode != 2 or quoted.stdout or quoted.stderr != expected:
                raise AssertionError('native argv quoting or diagnostic normalization differs')
            receipt['assertions'].append('native argv spaces/quotes/backslashes/Unicode and exit 2')
            result = run([binaries['vault_fixture']])
            if result.stdout.strip() != 'credential adapter compiled fixture passed':
                raise AssertionError('dummy vault fixture did not complete')
            receipt['assertions'].append('compiled dummy vault save/load/delete/absent (mock transport)')
            args.receipt.parent.mkdir(parents=True, exist_ok=True)
            child_receipt = args.receipt.resolve().with_suffix('.commands.json')
            subprocess.run([sys.executable, str(HERE / 'harness.py'), '--candidate-only',
                            '--candidate', str(binaries['sdk_fixture']), '--scratch', str(home),
                            '--receipt', str(child_receipt), '--deny-network', shlex.join(prefix)],
                           cwd=home, env=env, capture_output=True, text=True, timeout=180, check=True)
            commands = json.loads(child_receipt.read_text())
            if not commands['network_denied'] or not commands['cases'] or any('failure' in c for c in commands['cases']):
                raise AssertionError('SDK/child harness failed')
            receipt['commands_receipt'] = str(child_receipt)
            receipt['assertions'].append('prepared SDK read and native fixture child stdin/EOF/termination')
        receipt['packet_passed'] = True
    except Exception as error:
        receipt['packet_passed'] = False
        # Fixed diagnostics avoid copying subprocess output or ambient data.
        receipt['failure'] = str(error) if isinstance(error, (ValueError, AssertionError)) else type(error).__name__
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    args.receipt.write_text(json.dumps(receipt, indent=2) + '\n')
    print(f"{args.job}: {'passed' if receipt['packet_passed'] else 'failed'} ({args.execution})")
    return 0 if receipt['packet_passed'] else 1


if __name__ == '__main__':
    sys.exit(main())
