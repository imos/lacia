#!/usr/bin/env python3
"""Build/install a per-user Lacia app and its own LaunchAgent, without admin access."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import plistlib
import platform
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile

LABEL = 'jp.imoz.lacia.notifications'
SOURCE = Path(__file__).resolve().parent
HOME = Path.home()
STATE = HOME / 'Library/Application Support/Lacia Notifications'
APP = HOME / 'Applications/Lacia Notifications.app'
AGENT = HOME / 'Library/LaunchAgents' / (LABEL + '.plist')
LEGACY_SCHEDULE_LABEL = 'jp.imoz.lacia.schedule-worker'


def run(*args):
    subprocess.run([str(a) for a in args], check=True)


def stop(code, message):
    # Keep the exit marker available to callers and explain the error to the person running the tool.
    print(message, file=sys.stderr)
    raise SystemExit(code)


def private(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.is_symlink() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077:
        stop('private_directory_required', '保存先には、このMacの利用者本人が所有するディレクトリーを指定してください。シンボリックリンクは使えません。ほかの利用者にアクセス権がないことも確かめてください。')


def read_private(path):
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            stop('private_file_required', 'このMacの利用者本人が所有し、ほかの利用者にアクセス権がない通常のファイルを指定してください。')
        return source.read()


def write_private(path, data, resume=False):
    # Link a complete private file into place without overwriting any copy.
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as destination:
        draft = Path(destination.name)
        try:
            destination.write(data)
            destination.close()
            try:
                os.link(draft, path)
            except FileExistsError:
                if not resume or read_private(path) != data:
                    raise
        finally:
            draft.unlink(missing_ok=True)


def write_config(path, config):
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as destination:
        draft = Path(destination.name)
        try:
            destination.write((json.dumps(config, ensure_ascii=False, indent=2) + '\n').encode())
            destination.close()
            draft.replace(path)
        finally:
            draft.unlink(missing_ok=True)


def prepare_schedule(config, device, secret_source=None, probe=None, resume=False):
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]*', device) or device == 'local':
        stop('invalid_schedule_device', '端末の登録IDには英小文字、数字、ハイフンを使えます。英小文字か数字で始め、予約名の local とは別の名前を指定してください。')
    if not config.get('tasks_repo'):
        stop('schedule_requires_tasks_repo', 'このMacで予約を受け取るには、--tasks-repo で記録の読み取り専用コピーを指定してください。')
    destination = STATE / 'schedule-device.secret'
    secret = read_private(secret_source).strip() if secret_source else secrets.token_urlsafe(32).encode()
    if len(secret) < 32 or any(byte < 33 or byte > 126 for byte in secret):
        stop('invalid_schedule_secret', '端末の認証に使う値は、ASCII の英数字と記号で32文字以上にしてください。空白は使えません。')
    write_private(destination, secret + b'\n', resume)
    config['schedule'] = {'device': device, 'secret_file': destination.name}
    if probe:
        config['schedule']['auth_check'] = probe


def migrate_schedule(target):
    """Copy the stopped worker's enrollment and execution journal."""
    private(STATE)
    old_agent = AGENT.with_name(LEGACY_SCHEDULE_LABEL + '.plist')
    backup = STATE / 'legacy-schedule-agent.plist'
    config_path = STATE / 'config.json'
    config = json.loads(read_private(config_path))
    if config.get('schedule'):
        if backup.exists() and not old_agent.exists():
            return  # migration committed before an app upgrade was interrupted
        stop('schedule_already_configured', '通知アプリには、すでに予約を受け取る設定があります。アプリだけを更新するときは --migrate-schedule を外してください。')
    if old_agent.exists() and backup.exists():
        stop('schedule_migration_destination_exists', '引き継ぎ先に以前の予約の記録があります。引き継ぎ元と保存先の記録を確かめてください。')
    legacy = plistlib.loads(read_private(old_agent if old_agent.exists() else backup))
    argv = legacy.get('ProgramArguments', [])
    if legacy.get('Label') != LEGACY_SCHEDULE_LABEL or len(argv) < 2 or argv[1] != 'schedule-worker':
        stop('foreign_schedule_agent_preserved', '引き継ぎ元の常駐処理を、レイシアの予約用と確認できませんでした。登録先と起動コマンドを確かめてください。')
    if subprocess.run(['launchctl', 'print', target + '/' + LEGACY_SCHEDULE_LABEL],
                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        stop('stop_legacy_schedule_agent_first', '以前の予約用の常駐処理が動いています。その処理を止めてから引き継いでください。')
    old_config_path = Path(argv[argv.index('--config') + 1])
    old_state = Path(argv[argv.index('--state') + 1])
    old_config = json.loads(read_private(old_config_path))
    transport = config.get('transport', {})
    base = transport.get('url')
    if not base:
        port = transport['port'] if transport.get('kubectl') or transport.get('context') else 10148
        base = f'http://127.0.0.1:{port}'
    if old_config.get('url', '').rstrip('/') != base.rstrip('/'):
        stop('legacy_schedule_transport_differs', '通知アプリと以前の予約用の常駐処理で、サーバーの接続先が異なります。引き継ぐ前に両方の設定を確かめてください。')
    if Path(config.get('tasks_repo', '')).resolve() != (old_config_path.parent / old_config['root']).resolve():
        stop('legacy_schedule_repository_differs', '通知アプリと以前の予約用の常駐処理が、異なる記録のリポジトリーを使っています。引き継ぐ前に両方の設定を確かめてください。')
    journal = old_state / 'schedule-worker.json'
    new_journal = STATE / journal.name
    secret = old_config_path.parent / old_config['secret_file']
    lock = os.open(old_state / (journal.name + '.lock'), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock, 'rb'):
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            stop('stop_legacy_schedule_agent_first', '以前の予約用の常駐処理が動いています。その処理を止めてから引き継いでください。')
        if journal.exists():
            data = read_private(journal)
            if not isinstance(json.loads(data).get('runs'), dict):
                stop('invalid_legacy_schedule_state', '以前の予約の実行記録を読み取れませんでした。引き継ぎ元の schedule-worker.json の runs の内容を確かめてください。')
            write_private(new_journal, data, resume=True)
        elif new_journal.exists():
            stop('schedule_migration_destination_exists', '引き継ぎ先に以前の予約の記録があります。引き継ぎ元と保存先の記録を確かめてください。')
        prepare_schedule(config, old_config['device'], secret, old_config.get('auth_check'), resume=True)
        if old_agent.exists():
            old_agent.rename(backup)
        write_config(config_path, config)


def build(destination):
    if destination.exists():
        stop('build_destination_already_exists', 'アプリの出力先がすでにあります。build には、まだ使っていない出力先を指定してください。')
    binary = destination / 'Contents/MacOS/LaciaNotifier'
    resources = destination / 'Contents/Resources'
    binary.parent.mkdir(parents=True); resources.mkdir()
    run('xcrun', 'swiftc', '-swift-version', '5', '-target', platform.machine() + '-apple-macos14.0', '-O', '-framework', 'AppKit', '-framework', 'UserNotifications', '-framework', 'EventKit', SOURCE / 'LaciaNotifier.swift', '-o', binary)
    shutil.copy2(SOURCE / 'brand.png', resources / 'brand.png')
    # macOS caches app icons by file. A new name for new artwork makes the
    # notification banners pick up the change.
    artwork_sources = (SOURCE / 'brand.png').read_bytes() + (SOURCE / 'render-icon.swift').read_bytes()
    icon_name = 'brand-' + hashlib.sha256(artwork_sources).hexdigest()[:16] + '.icns'
    with tempfile.TemporaryDirectory() as temporary:
        artwork = Path(temporary) / 'icon.png'
        run('xcrun', 'swift', SOURCE / 'render-icon.swift', resources / 'brand.png', artwork)
        iconset = Path(temporary) / 'brand.iconset'; iconset.mkdir()
        for size in (16, 32, 128, 256, 512):
            for scale in (1, 2):
                name = f'icon_{size}x{size}' + ('@2x' if scale == 2 else '') + '.png'
                run('sips', '-z', size * scale, size * scale, artwork, '--out', iconset / name)
        run('iconutil', '-c', 'icns', iconset, '-o', resources / icon_name)
    plist = {'CFBundleIdentifier': LABEL, 'CFBundleName': 'レイシア通知', 'CFBundleDisplayName': 'レイシア通知',
             'CFBundleExecutable': 'LaciaNotifier', 'CFBundlePackageType': 'APPL', 'CFBundleVersion': '2',
             'CFBundleShortVersionString': '0.1.0', 'CFBundleIconFile': icon_name, 'LSUIElement': True,
             'LSMinimumSystemVersion': '14.0', 'NSUserNotificationAlertStyle': 'alert',
             'NSCalendarsFullAccessUsageDescription': '会議が始まる1分前にお知らせするため、このMacのカレンダーにある予定を読み取ります。予定の作成、変更、削除は行いません。'}
    (destination / 'Contents/Info.plist').write_bytes(plistlib.dumps(plist))
    run('codesign', '--force', '--sign', '-', '--identifier', LABEL, destination)
    run('codesign', '--verify', '--strict', destination)


def find_lacia(lacia):
    lacia = lacia or shutil.which('lacia')
    if not lacia or not os.access(lacia, os.X_OK):
        stop('lacia_binary_required', 'lacia の実行ファイルを使えません。実行権限を確かめ、--lacia でファイルの場所を指定してください。')
    return str(Path(lacia).resolve())


def upgrade(source, migrate=False):
    """Replace the installed app with a new build. The settings, the state and
    the notices already scheduled stay as they are."""
    if not (APP.exists() and AGENT.exists() and STATE.exists()):
        stop('no_installation_to_upgrade', '更新に必要なアプリ、常駐処理、設定のいずれかが見つかりません。このMacに初めて入れるときは install を使ってください。')
    for bundle in (source, APP):
        if plistlib.loads((bundle / 'Contents/Info.plist').read_bytes()).get('CFBundleIdentifier') != LABEL:
            stop('unexpected_bundle', '更新に使うアプリか、インストール済みのアプリの識別子がレイシア通知と異なります。更新元と更新先を確かめてください。')
    run('codesign', '--verify', '--strict', source)
    target = 'gui/' + str(os.getuid())
    if migrate:
        migrate_schedule(target)
    subprocess.run(['launchctl', 'bootout', target + '/' + LABEL], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    shutil.rmtree(APP)
    shutil.copytree(source, APP)
    run('/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister', '-f', APP)
    run('launchctl', 'bootstrap', target, AGENT)
    print('通知アプリを更新して起動しました。', file=sys.stderr)
    print('upgraded_and_started')


def install(source, lacia=None, url=None, tasks_repo=None, context=None, namespace=None,
            target=None, port=18190, remote_port=None, site=None, place=None,
            schedule_device=None, schedule_secret_file=None, schedule_auth_check=None):
    if APP.exists() or AGENT.exists() or STATE.exists():
        stop('existing_installation_preserved_use_documented_upgrade', '通知アプリか、その設定がすでにあります。インストール済みのアプリを更新するときは upgrade を使ってください。')
    # With --url the app talks to the configured server without kubectl.
    # With --context and --namespace it keeps a port-forward to the cluster.
    # With neither, the worker uses the local server. --site is the address a
    # notification opens in the browser, separate from the port-forward.
    forwarded = context or namespace or target or remote_port
    if url and forwarded:
        stop('url_and_cluster_are_exclusive', 'サーバーに直接つなぐときは --url を指定してください。ポート転送を使うときは --url を外し、クラスタの接続先を指定してください。')
    if forwarded and not (context and namespace and target and remote_port):
        stop('context_namespace_target_and_remote_port_required', 'ポート転送には --context、--namespace、--target、--remote-port の4つを指定してください。')
    kubectl = shutil.which('kubectl') if forwarded else None
    if forwarded and not kubectl:
        stop('kubectl_required', 'ポート転送に使う kubectl が見つかりません。PATH から実行できるか確かめてください。')
    if (schedule_secret_file or schedule_auth_check) and not schedule_device:
        stop('schedule_options_require_device', '予約の認証を設定するときは、--schedule-device にこのMacの登録IDを指定してください。')
    if schedule_device and (not tasks_repo or url and not url.startswith(('https://', 'http://127.0.0.1:', 'http://localhost:', 'http://[::1]:'))):
        stop('schedule_requires_tasks_repo_and_private_transport', 'このMacで予約を受け取るときは --tasks-repo を指定してください。接続には HTTPS、このMacのサーバー、ポート転送のいずれかを使ってください。')
    if plistlib.loads((source / 'Contents/Info.plist').read_bytes()).get('CFBundleIdentifier') != LABEL:
        stop('unexpected_source_bundle', 'インストール元のアプリの識別子がレイシア通知と異なります。build で作ったアプリを指定してください。')
    lacia = find_lacia(lacia)
    run('codesign', '--verify', '--strict', source)
    private(STATE)
    APP.parent.mkdir(exist_ok=True)
    shutil.copytree(source, APP)
    # "lacia" is what the app starts as its worker (`lacia agent`).
    cfg = {'enabled': True, 'task_notifications': True, 'meeting_notifications': True, 'meeting_source': 'linked',
           'exclude_meetings': [], 'lacia': lacia}
    if site or url:
        cfg['site'] = (site or url).rstrip('/')
    if url:
        cfg['transport'] = {'url': url.rstrip('/')}
    elif forwarded:
        cfg['transport'] = {'kubectl': kubectl, 'context': context, 'namespace': namespace, 'port': port,
                            'target': target, 'remote_port': remote_port}
    if tasks_repo:
        cfg['tasks_repo'] = str(tasks_repo)
    if place:
        cfg['place'] = place
    if schedule_device:
        probe = [str(schedule_auth_check.resolve())] if schedule_auth_check else None
        prepare_schedule(cfg, schedule_device, schedule_secret_file, probe)
    write_config(STATE / 'config.json', cfg)
    agent = {'Label': LABEL, 'ProgramArguments': [str(APP / 'Contents/MacOS/LaciaNotifier')],
             'RunAtLoad': True, 'KeepAlive': True, 'ThrottleInterval': 30, 'ProcessType': 'Interactive',
             'LimitLoadToSessionType': 'Aqua', 'Umask': 0o077,
             'StandardOutPath': str(STATE / 'agent.log'), 'StandardErrorPath': str(STATE / 'agent-error.log')}
    for name in ['agent.log', 'agent-error.log']:
        log = STATE / name; log.touch(mode=0o600)
    AGENT.parent.mkdir(exist_ok=True)
    AGENT.write_bytes(plistlib.dumps(agent)); AGENT.chmod(0o600)
    run('/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister', '-f', APP)
    print('通知アプリをインストールし、常駐用の設定を保存しました。起動するときは start を実行し、メニューバーで「通知を許可する」を選んでください。', file=sys.stderr)
    print('installed_app_and_agent; permissions_not_requested; not_started')


def main():
    parser = argparse.ArgumentParser(description="このMacでレイシアの通知アプリを管理します。アプリは、このMacか別の環境で動く lacia serve に接続します。")
    sub = parser.add_subparsers(dest='action', required=True)
    sub.add_parser('build', help='通知アプリを作って署名します。').add_argument('destination', type=Path, help='アプリの出力先を指定してください。')
    installer = sub.add_parser('install', help='通知アプリをこのMacに入れ、常駐用の設定を保存します。')
    installer.add_argument('source', type=Path, help='build で作った通知アプリを指定してください。')
    installer.add_argument('--lacia', type=Path, help='通知アプリが lacia agent を起動するときに使う実行ファイルです。省くと PATH から探します。')
    installer.add_argument('--url', help='lacia serve に直接接続するためのURLです。URLもポート転送も指定しなければ http://127.0.0.1:10148 を使います。')
    installer.add_argument('--site', help='通知を押したときに開くアドレスです。省くと --url の値か、ローカルのサーバーを使います。')
    installer.add_argument('--context', help='ポート転送で接続するときは、--url の代わりに kubectl のコンテキストを指定します。--namespace、--target、--remote-port も必要です。')
    installer.add_argument('--namespace', help='クラスタ内のサーバーの名前空間です。')
    installer.add_argument('--target', help='ポート転送の接続先です。--context を使うときに service/lacia などを指定します。')
    installer.add_argument('--port', type=int, default=18190, help='ポート転送に使うMac側のポート番号です。')
    installer.add_argument('--remote-port', type=int, help='接続先のポート番号です。--context と一緒に指定します。')
    installer.add_argument('--tasks-repo', type=Path, help='別のサーバーの記録と同期する場合は、このMacにある読み取り専用のコピーを指定します。このMacの記録に直接書き込む場合は省きます。')
    installer.add_argument('--place', help='申し送りを受け取る場所の名前です。省くと local を使います。')
    installer.add_argument('--schedule-device', help='別のサーバーから予約を受け取るときは、この端末の登録IDを指定します。このMacのサーバーが予約を動かす場合は省きます。')
    installer.add_argument('--schedule-secret-file', type=Path, help='端末の認証に使う値を保存した非公開のファイルです。端末を登録するときにこの指定を省くと、新しい値を作ります。')
    installer.add_argument('--schedule-auth-check', type=Path, help='認証の状態を調べる読み取り専用のコマンドです。利用者の認証が必要なときに終了コード 77 を返すコマンドを指定してください。')
    upgrader = sub.add_parser('upgrade', help='このMacの設定を引き継いで、通知アプリを更新します。')
    upgrader.add_argument('source', type=Path, help='更新に使う版の通知アプリを指定してください。')
    upgrader.add_argument('--migrate-schedule', action='store_true', help='以前の予約用の常駐処理から、端末の登録と実行の記録を引き継ぎます。引き継ぐ前に、その常駐処理を止めてください。')
    sub.add_parser('start', help='通知アプリを常駐させます。')
    sub.add_parser('stop', help='通知アプリの常駐を止め、まだ届いていない通知の予約を取り消します。')
    remover = sub.add_parser('uninstall', help='通知アプリと常駐用の設定を削除します。')
    remover.add_argument('--remove-state', action='store_true', help='このMacの設定と、同じ通知を繰り返さないための記録も削除します。lacia uninstall が使います。')
    args = parser.parse_args()
    target = 'gui/' + str(os.getuid())
    if args.action == 'build': build(args.destination.resolve())
    elif args.action == 'install':
        install(args.source.resolve(), args.lacia, args.url, args.tasks_repo, args.context, args.namespace,
                args.target, args.port, args.remote_port, args.site, args.place,
                args.schedule_device, args.schedule_secret_file, args.schedule_auth_check)
    elif args.action == 'upgrade': upgrade(args.source.resolve(), args.migrate_schedule)
    elif args.action == 'start': run('launchctl', 'bootstrap', target, AGENT)
    elif args.action == 'stop':
        run('launchctl', 'bootout', target + '/' + LABEL)
        run(APP / 'Contents/MacOS/LaciaNotifier', '--clear-pending')
    else:
        # Only remove paths whose ownership marker is our exact bundle ID/agent label.
        if APP.exists() and plistlib.loads((APP / 'Contents/Info.plist').read_bytes()).get('CFBundleIdentifier') != LABEL:
            stop('foreign_app_preserved', 'インストール先にあるアプリの識別子がレイシア通知と異なります。アプリを削除する前に、対象を確かめてください。')
        if AGENT.exists() and plistlib.loads(AGENT.read_bytes()).get('Label') != LABEL:
            stop('foreign_agent_preserved', '登録されている常駐処理の識別子がレイシア通知と異なります。削除する前に、対象を確かめてください。')
        subprocess.run(['launchctl', 'bootout', target + '/' + LABEL], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if APP.exists(): run(APP / 'Contents/MacOS/LaciaNotifier', '--clear-pending')
        AGENT.unlink(missing_ok=True)
        if APP.exists(): shutil.rmtree(APP)
        if args.remove_state:
            shutil.rmtree(STATE, ignore_errors=True)
            print('通知アプリと、常駐用の設定と、このMacの設定を削除しました。', file=sys.stderr)
            print('uninstalled; private_state_removed')
            return
        print('通知アプリと常駐用の設定を削除しました。同じ通知を繰り返さないための記録や、このMacの設定は手元に残っています。', file=sys.stderr)
        print('uninstalled; private_state_preserved_for_dedup_and_manual_removal')


if __name__ == '__main__': main()
