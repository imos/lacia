# レイシア（lacia）

`lacia` は、Codex や Claude Code を、秘書の「レイシア」として動かすための道具です。会話で受け付けた依頼について、進み具合や成果、オーナーさんに確かめてほしいことを記録します。

Web、コマンド、Discord から依頼すると、`lacia` が Codex か Claude Code の会話へ1件ずつ渡します。決まった時刻に繰り返す仕事は予約できます。Web の画面では、確認待ちのタスクや予定を一覧し、以前のやり取りも読み返せます。

タスクは Markdown ファイルに書きます。保存先は、使う人（オーナーさん）ごとに用意する「記録のリポジトリー」です。1つの実行ファイルに、タスクを読み書きするコマンド、会話が従う決まり（`lacia rules`）、Web サーバーが入っています。必要に応じて、記録の Git への自動保存、Google Calendar からの予定の取得、Mac への通知も使えます。

## 要るもの

- Go（ビルドに使います。版は `go.mod` にあります）
- Git
- [Codex](https://github.com/openai/codex) か [Claude Code](https://claude.com/claude-code) の、どちらか
- ひな形から複製した記録のリポジトリー

macOS 専用の通知アプリをビルドするときは、Xcode のコマンドラインツールも要ります。

## ローカル環境で始める

ふだん使う端末に記録を置き、そこでサーバーと Codex か Claude Code を動かします。まず、コードのリポジトリーで次のコマンドを `sh` から実行し、`lacia` をビルドします。

```sh
mkdir -p "$HOME/bin"
go build -o "$HOME/bin/lacia" ./cmd/lacia
```

記録のひな形を `~/records` に複製したら、レイシアに呼んでほしい名前を設定します。Google Calendar を使う場合は、ここで読むアカウントも入力します。

```sh
cd "$HOME/records"
"$HOME/bin/lacia" init
"$HOME/bin/lacia" serve --root "$HOME/records" --state "$HOME/.local/state/lacia" --claude claude
```

Codex を使う場合は、最後の `--claude claude` を `--codex codex` に置き換えます。起動したまま、ブラウザーで `http://127.0.0.1:10148/` を開きます。Web の画面から依頼し、進み具合を確かめられます。別のターミナルでコマンドを使うときも、同じ記録を開きます。

```sh
cd "$HOME/records"
"$HOME/bin/lacia" task list
```

この構成では、予約の会話もローカル環境のサーバーが起動します。予約の宛先は省けます。外への待ち受け、Git への自動保存、Discord との接続は既定で無効です。macOS で通知アプリを使う場合は、同じ環境で動くサーバーにつなぎます。 [予約](docs/design/schedules.md) · [Mac の通知](docs/design/notifications.md#インストール)

## 別の環境で Web サーバーを動かす

サーバーを動かす機械に `lacia`、会話の起動コマンド、サーバーが書き込む記録を用意します。その機械で記録の初期設定を済ませ、ローカル環境の例と同じ `lacia serve` を、そこにある記録と状態のパスで起動します。記録を分けて運用するときは、プロファイルごとに記録と状態を持たせます。 [設定の書き方](docs/design/settings.md#設定の書き方)

手元のブラウザーでサーバーの URL を開きます。直接接続できない場合は、許可されたポート転送を使えます。ターミナルから同じサーバーを操作するときは、接続先を `LACIA_API` に指定できます。次は、SSH でサーバーの既定のポートに接続する例です。

```sh
# 手元のターミナルで、Web を開くあいだ動かしておきます
ssh -N -L 18190:127.0.0.1:10148 example.com
```

この例では、手元のブラウザーで `http://127.0.0.1:18190/` を開きます。サーバーを外から開けるアドレスに置く場合は、手前のプロキシなどで認証を行います。 [外から受け付ける入口](docs/design/profiles.md#外から受け付ける入口)

手元の端末から `lacia task list` や `lacia task show` でサーバーの最新の内容を読む場合は、手元の記録を読み取り専用のコピーとして設定します。 [読み取り専用のコピー](docs/design/records.md#読み取り専用のコピー)

サーバーとは別の端末で会話を起動する予約や申し送りを使うときは、その端末を登録します。macOS の通知アプリを使う場合は、別の環境のサーバーにつなぎます。Kubernetes の `kubectl port-forward` は、クラスタ内のサーバーへ接続する方法の1つです。 [Mac の通知](docs/design/notifications.md#接続先を決める引数) · [別の端末で予約を受け取る](docs/design/schedules.md#別の端末で予約を受け取る)

記録のリポジトリーは、少なくとも、タスクを置くディレクトリー（`areas/work/tasks/` や `work/tasks/` など）か、`.lacia/config.json` を持つ Git のリポジトリーです。 [記録の形](docs/design/records.md)

## コードを直しながら使う

`lacia` のコードを自分で直しながら使うときは、コードのリポジトリーを、記録のリポジトリーの `.lacia/source/lacia/` にクローンします。記録の `.gitignore` に `/.lacia/source/` の行を書いておくと、このクローンは記録の Git に入らず、サーバーの自動保存にも取り込まれません。行がないときは、`lacia doctor` が知らせます。

```sh
cd "$HOME/records"
git clone <公式のリポジトリーの URL> .lacia/source/lacia
cd .lacia/source/lacia
go build -o "$HOME/bin/lacia.new" ./cmd/lacia && mv "$HOME/bin/lacia.new" "$HOME/bin/lacia"
```

- 公式の `main` をそのまま使うときは、上のように公式のリポジトリーをクローンします。フォークで運用するときは、フォークを `origin`、公式を `upstream` にします。自分の変更は `origin` へプッシュし、公式の変更は `upstream` から取り込みます。
- ビルドした実行ファイルは、`PATH` の通った場所に置きます。会話が `lacia` を使っているあいだに置き換えるときは、上の例のように別の名前でビルドしてから `mv` で入れ替えます。
- 別の環境で動かすサーバーには、ソースを置きません。コミットしていない変更がない状態で、確定したコミットからビルドした実行ファイルだけを送ります。どのコミットで動いているかは、`lacia serve` が起動のときにログへ書く `version` と、Web の画面のいちばん下の「版」で確かめられます。コミットしていない変更を含めて作った実行ファイルでは、版に「（未コミットの変更あり）」と出ます。
- タスクのために作業ツリーを分けるときは、記録の `local/worktrees/<タスクID>/` に作ります。たとえば、`git worktree add -b <ブランチ> "$HOME/records/local/worktrees/T1001"` のように作ります。`local/` は Git に入りません。取り込みが済んだら、`git worktree remove` で片づけます。

## コマンド

`lacia help` で、すべてのコマンドと引数が出ます。

| コマンド | すること |
| --- | --- |
| `lacia task list`、`show`、`log` | タスクを読みます |
| `lacia task new`、`set`、`field`、`note`、`attach`、`claim` | タスクを作り、状態を変え、履歴や成果物を足します |
| `lacia task lint` | タスクの文章を点検します |
| `lacia ask` | レイシアへ依頼を送ります |
| `lacia rules` | 会話が従う決まりを出します |
| `lacia init` | 記録のリポジトリーに、呼び名と、予定を読むアカウントを書きます |
| `lacia codex`、`lacia claude` | 決まりと、きょうの状況を渡して、会話を起動します |
| `lacia serve` | サーバーを動かします |
| `lacia schedules`、`lacia todo` | 予約の定義を確かめ、`TODO.md` を作り直します |
| `lacia project note` | プロジェクトの履歴に書きます |
| `lacia doctor` | 記録の形を点検します。直さずに、誤りとお知らせを出します |
| `lacia migrate` | 記録を形式0から形式1へ移します。`--undo` で戻せます |
| `lacia notice`、`lacia self` | `notice` で心が動いたことを書き留め、`self` でいまの記録と、次の書き直しに使う書き留めを読みます |
| `lacia who` | この会話の名前と、いま動いている会話の一覧を表示します |
| `lacia agent` | サーバーから通知用の情報を読み、登録済みの端末に届く予約と申し送りも受け取ります。macOS の通知アプリも、このコマンドを起動します |
| `lacia peer`、`lacia handoff` | `peer` で相手のプロファイルに伝言を送り、`handoff` でタスクの側へ用件を引き継ぎます |
| `lacia discord` | Discord で依頼を受け付ける人を確かめたり、受け付けをしばらく止めたり、返事に PDF を添えたりできます |
| `lacia wait`、`lacia job run`、`lacia tell` | `wait` で計算の終了を待ちます。`job run` はジョブの投入と待ちの登録をまとめて行い、`tell` は別の会話へ依頼を届けます |
| `lacia shims` | 実行ファイルに埋め込んであるコマンドを書き出します |

タスクを読み書きするには形式1の記録を使います。以前の形式0から移すときは、サーバーを止め、`lacia migrate --dry-run` で計画を確かめてから `lacia migrate` を実行してください。移行後は同じ `lacia` で `lacia doctor` を実行し、そのまま使い始められます。`lacia migrate --undo` で形式0に戻すと、もう1度移行するまで通常のタスク操作はできません。

## 設定

`lacia serve` の設定は、コマンドラインの引数か、設定ファイル（`--config`）に書きます。一覧は、[設定の一覧](docs/design/settings.md)にあります。

## 文書

設計の説明は、[docs/design/](docs/design/README.md) にあります。はじめは、[全体の構成](docs/design/overview.md)から読んでください。

| 文書 | 中身 |
| --- | --- |
| [全体の構成](docs/design/overview.md) | 部品と、役割の分け方 |
| [記録のリポジトリー](docs/design/records.md) | タスクのファイルの形と、記録の設定 |
| [決まりと会話の起動](docs/design/rules.md) | `lacia rules`、`lacia init`、`lacia codex`、`lacia claude` |
| [依頼の受付](docs/design/requests.md) | 依頼が会話へ届くまで |
| [設定の一覧](docs/design/settings.md) | `lacia serve` の設定と、記録の設定 |
| [開発の決まり](AGENTS.md) | このリポジトリーで作業するときの決まり |

## ライセンス

レイシア本体は、個人が自分の生活や仕事のために利用できます。販売や共有に関する条件は、[ライセンス](LICENSE)をご覧ください。同梱物や依存ライブラリーの出典と条件は、[第三者の権利表示](THIRD_PARTY_NOTICES.md)にあります。

## コードの場所

| 場所 | 中身 |
| --- | --- |
| `cmd/lacia/` | コマンドの受付、サーバーの起動と各機能の初期設定 |
| `internal/task/` | タスクの読み書き、ID の採番、文章の点検 |
| `internal/web/` | Web の画面と API |
| `internal/request/`、`internal/codex/`、`internal/claudecode/` | 依頼の待ち行列と、Codex や Claude Code とのやり取り |
| `internal/schedule/` | 予約 |
| `internal/rules/` | 実行ファイルに埋め込む決まりと、記録の側での上書き |
| `internal/self/` | 自分についての記録 |
| `internal/calendar/`、`internal/gcal/` | 予定の表示と、Google Calendar からの取得 |
| `internal/gitsync/`、`internal/todo/` | Git への保存と、`TODO.md` の作成 |
| `internal/discord/`、`internal/daylog/` | Discord とのやり取りと、日ごとのログ |
| `internal/wait/`、`internal/agent/`、`internal/notify/` | 終了を待つ仕事、端末で動く `lacia agent` と通知の処理 |
| `internal/caller/` | コマンドを呼んだ会話の見分け |
| `internal/login/`、`cmd/lacia/shims/` | ログインの違う Codex の呼び分けと、実行ファイルに埋め込んだコマンド |
| `macos/notifier/` | Mac のメニューバーのアプリ |
| `scripts/update-holidays.py` | 祝日のデータを取り直すスクリプト |

## 試し方

```sh
gofmt -l cmd internal
go vet ./...
go test ./...
```
