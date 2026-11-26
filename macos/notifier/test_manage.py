import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import manage


class ScheduleEnrollmentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.state = root / 'notifier'
        self.legacy = root / 'legacy'
        self.repo = root / 'records'
        self.agent = root / 'agents' / 'notifier.plist'
        self.config = self.state / 'config.json'
        self.legacy_agent = self.agent.with_name(manage.LEGACY_SCHEDULE_LABEL + '.plist')
        for directory in (self.state, self.legacy, self.repo, self.agent.parent):
            directory.mkdir(mode=0o700)
        settings = {'STATE': self.state, 'AGENT': self.agent}
        patches = patch.multiple(manage, **settings)
        patches.start()
        self.addCleanup(patches.stop)
        self.base = 'http://127.0.0.1:12345'
        self.initial = {'tasks_repo': str(self.repo), 'transport': {'url': self.base}}
        manage.write_config(self.config, self.initial)

    def legacy_fixture(self):
        secret = self.legacy / 'fixture-secret'
        config = self.legacy / 'worker.json'
        manage.write_private(secret, b'a' * 32 + b'\n')
        manage.write_config(config, {'device': 'fixture', 'root': str(self.repo), 'url': self.base,
                                      'secret_file': secret.name, 'auth_check': ['fixture-probe']})
        manage.write_private(self.legacy_agent, plistlib.dumps({
            'Label': manage.LEGACY_SCHEDULE_LABEL,
            'ProgramArguments': ['fixture-lacia', 'schedule-worker', '--config', str(config),
                                 '--state', str(self.legacy)],
        }))
        self.journal = b'{"runs":{"sched-fixture":{"claim":"fixture","phase":"prepared"}}}'
        manage.write_private(self.legacy / 'schedule-worker.json', self.journal)

    def test_new_device_uses_private_random_secret(self):
        manage.prepare_schedule(self.initial, 'fixture')
        self.assertEqual(self.initial['schedule'], {'device': 'fixture', 'secret_file': 'schedule-device.secret'})
        secret = self.state / 'schedule-device.secret'
        self.assertEqual(secret.stat().st_mode & 0o777, 0o600)
        self.assertGreaterEqual(len(manage.read_private(secret).strip()), 32)

    def test_migration_resumes_after_the_old_launch_agent_was_archived(self):
        self.legacy_fixture()
        with patch.object(manage.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1)):
            with patch.object(manage, 'write_config', side_effect=OSError('fixture interruption')):
                with self.assertRaises(OSError):
                    manage.migrate_schedule('gui/fixture')
            self.assertEqual(json.loads(self.config.read_bytes()), self.initial)
            self.assertFalse(self.legacy_agent.exists())
            manage.migrate_schedule('gui/fixture')
            manage.migrate_schedule('gui/fixture')
        self.assertEqual(json.loads(self.config.read_bytes())['schedule']['auth_check'], ['fixture-probe'])
        self.assertEqual((self.state / 'schedule-worker.json').read_bytes(), self.journal)
        self.assertEqual((self.legacy / 'schedule-worker.json').read_bytes(), self.journal)
        self.assertTrue((self.state / 'legacy-schedule-agent.plist').exists())

    def test_loaded_old_launch_agent_is_preserved(self):
        self.legacy_fixture()
        with patch.object(manage.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)):
            with self.assertRaisesRegex(SystemExit, 'stop_legacy_schedule_agent_first'):
                manage.migrate_schedule('gui/fixture')
        self.assertTrue(self.legacy_agent.exists())
        self.assertEqual(json.loads(self.config.read_bytes()), self.initial)
        self.assertFalse((self.state / 'schedule-worker.json').exists())


if __name__ == '__main__':
    unittest.main()
