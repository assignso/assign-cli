"""Deterministic orchestration checks; no native qualification or provider calls."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('packet', Path(__file__).with_name('native-packet.py'))
packet = importlib.util.module_from_spec(spec)
spec.loader.exec_module(packet)


class PacketTests(unittest.TestCase):
    def test_six_distinct_hosts(self):
        jobs = json.loads((packet.HERE / 'native-jobs.json').read_text())
        self.assertFalse(jobs['automatic_provisioning'])
        self.assertEqual(len({j['id'] for j in jobs['jobs']}), 6)

    def test_shims_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            shim = Path(temp) / 'vendor.cmd'
            shim.touch()
            with self.assertRaises(ValueError):
                packet.executable(shim)

    def test_packet_and_failure_receipts(self):
        for execution, fail, expected in [('native', False, 0), ('emulated', False, 0),
                                           ('native', True, 1), ('unavailable', False, 1)]:
            with self.subTest(execution=execution, fail=fail), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                binary = root / 'prepared.exe'
                binary.touch()
                identity = root / 'sdk.json'
                identity.write_text(json.dumps({'sdk_revision': 'd12065d63d9dd327b2e42f14ada155822f31702e'}))
                receipt = root / 'results' / 'packet.json'
                argv = ['packet', '--job', 'macos-arm64', '--execution', execution,
                        '--ordinary', str(binary), '--sdk-fixture', str(binary),
                        '--vault-fixture', str(binary), '--sdk-identity', str(identity),
                        '--scratch', str(root / 'scratch'), '--receipt', str(receipt)]
                calls = []
                def run(command, **kwargs):
                    calls.append((command, kwargs))
                    self.assertNotIn('ASSIGN_TOKEN', kwargs['env'])
                    if fail:
                        raise subprocess.CalledProcessError(1, command)
                    if '--candidate-only' in command:
                        Path(command[command.index('--receipt') + 1]).write_text(json.dumps(
                            {'network_denied': True, 'cases': [{'case': 'prepared'}]}))
                    output = 'credential adapter compiled fixture passed\n' if len(calls) == 4 else 'prepared\n'
                    return subprocess.CompletedProcess(command, 0, output, '')
                with patch.object(sys, 'argv', argv), patch.object(packet.platform, 'system', return_value='Darwin'), \
                     patch.object(packet.platform, 'machine', return_value='arm64'), \
                     patch.object(sys, 'platform', 'darwin'), patch.object(packet.subprocess, 'run', side_effect=run):
                    self.assertEqual(packet.main(), expected)
                result = json.loads(receipt.read_text())
                self.assertFalse(result['native_qualification'])
                self.assertEqual(result['packet_passed'], expected == 0)
                if expected == 0:
                    self.assertEqual(len(result['assertions']), 5)
                    self.assertEqual(len(calls), 5)


if __name__ == '__main__':
    unittest.main()
