#!/usr/bin/env python3
"""Move definitions and traceable logs without creating or replaying runs."""
import argparse
import json
import os
from pathlib import Path
import re
import sys
import tempfile

ID = re.compile(r"S[1-9][0-9]*\Z")
SLUG = re.compile(r"[a-z0-9][a-z0-9-]*\Z")


def load_mapping(path):
    rows = json.loads(Path(path).read_text())
    ids, names = set(), set()
    for row in rows:
        if not ID.fullmatch(row['id']) or not SLUG.fullmatch(row['legacy_name']):
            raise ValueError('対応表のIDか旧名が正しくありません。')
        if row['id'] in ids or row['legacy_name'] in names:
            raise ValueError('対応表のIDか旧名が重複しています。')
        if row.get('output', 'none') not in ('matters', 'task', 'none'):
            raise ValueError('output は matters、task、none から選んでください。')
        ids.add(row['id'])
        names.add(row['legacy_name'])
    return rows


def frontmatter(text, row):
    if not text.startswith('---\n') or '\n---\n' not in text[4:]:
        raise ValueError('定義の冒頭の項目を読めません。')
    end = text.index('\n---\n', 4)
    fields = text[4:end].splitlines()
    reserved = {'id', 'legacy_name', 'area', 'output'}
    if any(line.split(':', 1)[0] in reserved for line in fields):
        raise ValueError('旧定義に新形式の項目があります。手で照合してください。')
    added = [f'id: {row["id"]}', f'legacy_name: {row["legacy_name"]}',
             f'output: {row.get("output", "none")}']
    if row.get('area'):
        added.append('area: ' + json.dumps(row['area'], ensure_ascii=False))
    return '---\n' + '\n'.join(added + fields) + text[end:]


def relocate_links(text, old_path, new_path):
    # Markdown links stay valid after the definition moves one level deeper.
    def link(match):
        target = match[1]
        if re.match(r'[a-zA-Z][a-zA-Z0-9+.-]*:', target) or target.startswith(('/', '#', '<')):
            return match[0]
        path, marker, fragment = target.partition('#')
        absolute = os.path.normpath(str(old_path.parent / path))
        relative = os.path.relpath(absolute, new_path.parent)
        return '](' + relative + (marker + fragment if marker else '') + ')'
    return re.sub(r'\]\(([^\s)]+)\)', link, text)


def rewrite_paths(text, aliases):
    for slug, rid in aliases.items():
        text = text.replace(f'schedules/{slug}.md', f'routines/{rid}/routine.md')
        text = re.sub(r'/(?:schedules|routines)/'+re.escape(slug)+r'(?=[/#?\s)`]|$)', '/routines/'+rid, text)
        text = text.replace(f'routines/{slug}/', f'routines/{rid}/')
    return text


def plan(root, rows, state_dir=None):
    changes = {}
    aliases = {r['legacy_name']: r['id'] for r in rows}
    config = root / '.lacia' / 'config.json'
    if config.exists() and json.loads(config.read_text()).get('id_prefix') == 'S':
        raise ValueError('通常タスクの採番がS系列を使っています。先に採番設定を変更してください。')
    # All conflicts are detected before the first file is written.
    for row in rows:
        if list(root.glob('areas/*/tasks/*/'+row['id']+'/task.md')):
            raise ValueError('通常タスクと固定IDが衝突しています。')
    for base in (root / 'schedules', root / 'routines'):
        if base.is_symlink() or any(p.is_symlink() for p in base.rglob('*')):
            raise ValueError('定義か履歴の置き場所にシンボリックリンクがあります。')
    for row in rows:
        slug, rid = row['legacy_name'], row['id']
        source = root / 'schedules' / (slug + '.md')
        destination = root / 'routines' / rid / 'routine.md'
        if destination.exists():
            text = destination.read_text()
            if not re.search(r'^legacy_name: '+re.escape(slug)+r'$', text, re.M) or not re.search(r'^id: '+rid+r'$', text, re.M):
                raise ValueError(f'{destination} の対応が一致しません。')
            if source.exists():
                expected = rewrite_paths(relocate_links(frontmatter(source.read_text(), row), source, destination), aliases)
                if expected != text:
                    raise ValueError(f'{destination} と旧定義が一致しません。')
        elif source.exists():
            changes[destination] = rewrite_paths(relocate_links(frontmatter(source.read_text(), row), source, destination), aliases).encode()
        else:
            raise ValueError(f'{slug} の定義がありません。')
        if source.exists():
            changes[source] = None
        old_dir, new_dir = root / 'routines' / slug, root / 'routines' / rid
        if old_dir.exists():
            for old in sorted(old_dir.rglob('*')):
                if old.is_symlink():
                    raise ValueError('移行元にシンボリックリンクがあります。')
                if not old.is_file():
                    continue
                new = new_dir / old.relative_to(old_dir)
                if new in changes or (new.exists() and new.read_bytes() != old.read_bytes()):
                    raise ValueError(f'{new} に別の履歴があります。')
                if not new.exists():
                    changes[new] = old.read_bytes()
                changes[old] = None
    # Rewrite only explicit references, leaving external service IDs and ledger
    # contents intact. Archive is immutable and remains a historical reference.
    for path in sorted(root.rglob('*.md')):
        rel = path.relative_to(root)
        if any(p in ('archive', 'local', '.git', 'node_modules') for p in rel.parts) or path.is_symlink():
            continue
        if path in changes and changes[path] is None:
            continue
        text = changes.get(path, path.read_bytes()).decode()
        updated = rewrite_paths(text, aliases)
        if updated != text:
            changes[path] = updated.encode()
    if state_dir:
        boundary = state_dir / 'schedules.json'
        if boundary.exists():
            data = json.loads(boundary.read_text())
            for slug, rid in aliases.items():
                if slug in data:
                    data[rid] = max(data.get(rid, ''), data[slug])
                    # Keep the legacy key for rollback and older request IDs.
            encoded = (json.dumps(data, ensure_ascii=False, indent=2)+'\n').encode()
            if json.loads(boundary.read_text()) != data:
                changes[boundary] = encoded
        pending = state_dir / 'schedule-runs.json'
        if pending.exists():
            data = json.loads(pending.read_text())
            old = json.dumps(data)
            for run in data or []:
                run['name'] = aliases.get(run['name'], run['name'])
            if json.dumps(data) != old:
                changes[pending] = (json.dumps(data, ensure_ascii=False, indent=2)+'\n').encode()
        dispatch = state_dir / 'schedule-dispatch.json'
        if dispatch.exists():
            data = json.loads(dispatch.read_text())
            old = json.dumps(data)
            for execution in data['executions'].values():
                job = execution.get('job', {})
                slug = job.get('Name')
                if slug in aliases:
                    job['Name'], job['ID'], job['LegacyName'] = aliases[slug], aliases[slug], slug
            if json.dumps(data) != old:
                changes[dispatch] = (json.dumps(data, ensure_ascii=False, indent=2)+'\n').encode()
    manifest = root / 'routines' / 'migration-map.json'
    content = (json.dumps(rows, ensure_ascii=False, indent=2)+'\n').encode()
    if manifest.exists():
        previous = load_mapping(manifest)
        if any(row not in rows for row in previous):
            raise ValueError('保存済みの対応表と一致しません。')
    if not manifest.exists() or manifest.read_bytes() != content:
        changes[manifest] = content
    return changes


def apply(changes, backup):
    # Every before image is durable before the first mutation. Re-running after
    # interruption completes the same plan; the original backup is never lost.
    backup.mkdir(parents=True, exist_ok=False, mode=0o700)
    entries = []
    for number, (path, value) in enumerate(changes.items()):
        if path.is_symlink():
            raise ValueError('移行先にシンボリックリンクがあります。')
        before = path.read_bytes() if path.exists() else None
        entries.append({'path': str(path), 'before': str(number) if before is not None else None})
        if before is not None:
            p = backup / str(number)
            fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, 'wb') as out:
                out.write(before)
                out.flush()
                os.fsync(out.fileno())
    with (backup / 'journal.json').open('w') as out:
        out.write(json.dumps(entries, indent=2)+'\n')
        out.flush()
        os.fsync(out.fileno())
    for path, value in changes.items():
        if value is None:
            path.unlink(missing_ok=True)
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            fd, temp = tempfile.mkstemp(prefix='.routine-migration-', dir=path.parent)
            try:
                with os.fdopen(fd, 'wb') as out:
                    out.write(value)
                    out.flush()
                    os.fsync(out.fileno())
                os.replace(temp, path)
            finally:
                Path(temp).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--mapping', type=Path, required=True)
    parser.add_argument('--state-dir', type=Path)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--stopped', action='store_true', help='サーバーとワーカーの新規受付を停止済みです')
    parser.add_argument('--backup', type=Path, help='Git管理外の新しいバックアップ先です')
    args = parser.parse_args()
    root = args.root.resolve()
    if args.apply and (not args.stopped or not args.backup or not args.state_dir):
        parser.error('--apply には --stopped、--state-dir、--backup が必要です。')
    config = root / '.lacia' / 'config.json'
    gitconfig = root / '.git' / 'config'
    if args.apply and ((config.exists() and json.loads(config.read_text()).get('read_only')) or (gitconfig.is_file() and 'disabled://' in gitconfig.read_text())):
        parser.error('読み取り専用のコピーには書き込めません。')
    changes = plan(root, load_mapping(args.mapping), args.state_dir.resolve() if args.state_dir else None)
    print(f'{len(changes)}件のファイルを変えます。')
    for path, content in changes.items():
        print(('削除 ' if content is None else '保存 ') + str(path))
    if args.apply and changes:
        backup = args.backup.resolve()
        if not backup.is_relative_to(root / 'local'):
            parser.error('バックアップ先はリポジトリーの local/ の下にしてください。')
        apply(changes, backup)
        print('移しました。コミットも実行の受付もしていません。')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as error:
        sys.exit(str(error))
