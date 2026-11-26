import importlib.util
import json
from pathlib import Path
import tempfile
import subprocess
import sys
import unittest

spec = importlib.util.spec_from_file_location('migration', Path(__file__).with_name('migrate-routines.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class MigrationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.rows = [{'id': 'S1001', 'legacy_name': 'github', 'area': 'work', 'output': 'matters'}]
        self.put('schedules/github.md', '---\ntitle: 確認します\ndaily: 09:00\nenabled: false\n---\n[手順](../procedures/github.md)を読みます。\n')

    def put(self, path, text):
        path = self.root / path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def test_preserves_pending_execution_and_review_boundaries(self):
        request = 'sched-github-2026-11-25-0900'
        self.put('routines/github/runs/2026-11/runs.jsonl', '{"request":"'+request+'"}\n')
        self.put('routines/github/reviews/2026-11/reviews.jsonl', '{"confirmed":7,"notified":8}\n')
        self.put('state/schedules.json', '{"github":"2026-11-25T09:00","_tick":"2026-11-25T00:01:00Z"}')
        self.put('state/schedule-runs.json', json.dumps([{'name':'github', 'request':request}]))
        execution = {'id':request,'claim':'opaque-claim', 'job':{'Name':'github'}, 'status':'running', 'result':{'Settled':False}}
        self.put('state/schedule-dispatch.json', json.dumps({'executions':{request:execution}}))
        changes = m.plan(self.root, self.rows, self.root / 'state')
        m.apply(changes, self.root / 'local/backup')
        definition = (self.root / 'routines/S1001/routine.md').read_text()
        self.assertIn('enabled: false', definition)
        self.assertIn('../../procedures/github.md', definition)
        self.assertEqual((self.root / 'routines/S1001/reviews/2026-11/reviews.jsonl').read_text(), '{"confirmed":7,"notified":8}\n')
        data = json.loads((self.root / 'state/schedule-dispatch.json').read_text())['executions']
        self.assertEqual(list(data), [request])
        actual = data[request]
        for key in ['id', 'claim', 'status', 'result']:
            self.assertEqual(actual[key], execution[key])
        self.assertEqual(actual['job']['Name'], 'S1001')
        self.assertEqual(json.loads((self.root/'state/schedules.json').read_text())['S1001'], '2026-11-25T09:00')
        self.assertEqual(m.plan(self.root, self.rows, self.root / 'state'), {})
        self.assertEqual((self.root / 'local/backup/1').stat().st_mode & 0o777, 0o600)

    def test_conflict_does_not_mutate_files(self):
        self.put('routines/S1001/runs/2026-11/runs.jsonl','new\n')
        self.put('routines/github/runs/2026-11/runs.jsonl','old\n')
        with self.assertRaises(ValueError):
            m.plan(self.root, self.rows)
        self.assertTrue((self.root / 'schedules/github.md').exists())
        self.assertFalse((self.root / 'routines/S1001/routine.md').exists())

    def test_partial_move_can_resume_without_id_allocation(self):
        changes = m.plan(self.root, self.rows)
        destination = self.root/'routines/S1001/routine.md'
        destination.parent.mkdir(parents=True)
        destination.write_bytes(changes[destination])
        self.assertIn(self.root/'schedules/github.md', m.plan(self.root, self.rows))

    def test_cross_definition_links_are_moved_on_first_pass(self):
        self.put('schedules/calendar.md', '---\ndaily: 10:00\n---\n[確認](github.md)を読みます。\n')
        self.rows.append({'id':'S1002','legacy_name':'calendar','output':'none'})
        changes=m.plan(self.root,self.rows)
        m.apply(changes,self.root/'local/backup')
        self.assertEqual(m.plan(self.root,self.rows),{})
        text=(self.root/'routines/S1002/routine.md').read_text()
        self.assertIn('../../routines/S1001/routine.md',text)

    def test_pilot_can_expand_without_reassigning_ids(self):
        m.apply(m.plan(self.root, self.rows), self.root / 'local/pilot-backup')
        self.put('schedules/calendar.md', '---\ndaily: 10:00\n---\n確認します。\n')
        expanded = self.rows + [{'id': 'S1002', 'legacy_name': 'calendar', 'output': 'none'}]
        m.apply(m.plan(self.root, expanded), self.root / 'local/full-backup')
        self.assertEqual(json.loads((self.root / 'routines/migration-map.json').read_text()), expanded)
        self.assertEqual(m.plan(self.root, expanded), {})
        with self.assertRaises(ValueError):
            m.plan(self.root, self.rows)

    def test_web_links_point_directly_to_fixed_id(self):
        self.assertEqual(m.rewrite_paths(
            "[履歴](/schedules/github?month=2026-10) [定義](/routines/github) [別名](/schedules/github-other)",
            {"github": "S1001"}),
            "[履歴](/routines/S1001?month=2026-10) [定義](/routines/S1001) [別名](/schedules/github-other)")

    def test_task_id_collision_is_rejected(self):
        self.put('areas/work/tasks/2026-11/S1001/task.md', '---\nid: S1001\n---\n')
        with self.assertRaises(ValueError):
            m.plan(self.root,self.rows)

    def test_read_only_copy_cannot_be_applied(self):
        self.put('.git/config', '[remote "origin"]\n pushurl = disabled://server-only\n')
        mapping=self.put('map.json',json.dumps(self.rows))
        result=subprocess.run([sys.executable,str(Path(m.__file__)), '--root',str(self.root), '--mapping',str(mapping),'--state-dir',str(self.root/'local/state'), '--apply','--stopped','--backup',str(self.root/'local/backup')],capture_output=True,text=True)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('読み取り専用',result.stderr)
        self.assertTrue((self.root/'schedules/github.md').exists())

    def test_duplicate_mapping_is_rejected(self):
        path = self.put('map.json', json.dumps(self.rows + self.rows))
        with self.assertRaises(ValueError):
            m.load_mapping(path)


if __name__ == '__main__':
    unittest.main()
