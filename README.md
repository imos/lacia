# レイシア（lacia）

`lacia` は、Codex や Claude Code を、秘書の「レイシア」として動かすための道具です。会話で受け付けた依頼について、進み具合や成果、オーナーさんに確かめていただきたいことを記録します。

Web、コマンド、Discord から依頼すると、`lacia` が Codex か Claude Code の会話へ1件ずつ渡します。決まった時刻に繰り返す仕事は予約できます。Web では、確認待ちのタスクや予定を一覧で確かめたり、以前のやり取りを読み返したりできます。

タスクは Markdown ファイルに書きます。保存先は、使う人（オーナーさん）ごとに用意する「記録のリポジトリー」です。1つの実行ファイルに、タスクを読み書きするコマンド、会話が従う決まり（`lacia rules`）、Web サーバーが入っています。必要に応じて、記録を Git に自動で保存したり、Google Calendar から予定を取得したり、Mac で通知を受け取ったりできます。

## ローカル環境で始める

Mac に入れる手順は、[導入の案内](docs/install-guide.md)にあります。GitHub で空の非公開のリポジトリーを作り、`gh auth login` と `gh auth setup-git` で GitHub へつないでから、次のどちらかを実行します。

Apple シリコンの Mac では、導入のスクリプトを使えます。最新のリリースから実行ファイルを取得し、チェックサムの一覧と照らしてから、`lacia setup` を呼びます。

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/imos/lacia/main/install.sh | sh
```

ほかの Mac や Linux、ソースからビルドしたいときは、本体をクローンします。Go と `git` が要ります。

```sh
git clone https://github.com/imos/lacia.git ~/.lacia/src
make -C ~/.lacia/src install
```

どちらも、最後に `lacia setup` が、記録のリポジトリーの URL、記録を置く場所、わたしがお呼びする名前を尋ねます。記録の初期の中身を置いてプッシュし、ログインのときにサーバーが起動するよう登録して、ブラウザーでホームの画面（`http://127.0.0.1:10148/`）を開きます。組織の OAuth クライアントは、環境変数 `LACIA_GOOGLE_CLIENT_ID` と `LACIA_GOOGLE_CLIENT_SECRET` で渡します。 [導入の案内](docs/install-guide.md) · [導入と更新](docs/design/install.md)

毎日 04:00 に、夜の処理（「わたしのこと」の書き直し）が、Claude Code があれば Claude Code で、なければ Codex で動きます。その日のやり取りと記録から読んだファイルを、モデルの提供元へ送ります。止めるときは、記録の `.lacia/serve.conf` に `self-at = off` と書きます。 [夜の処理](docs/install-guide.md#夜の処理) · [わたしのこと](docs/design/self.md)

新しい版にするときは `lacia update`、やめるときは `lacia uninstall` を実行します。`lacia uninstall` は、記録と GitHub のリポジトリーを残します。

## 別の環境で Web サーバーを動かす

サーバーを動かす機械に `lacia`、会話の起動コマンド、サーバーが書き込む記録を用意します。その機械で記録の初期設定（`lacia init`）を済ませ、`lacia serve` を、そこにある記録と状態のパスで起動します。記録を分けて運用するときは、プロファイルごとに記録と状態を持たせます。 [設定の書き方](docs/design/settings.md#設定の書き方)

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
mkdir -p ~/.lacia && ln -s "$HOME/records/.lacia/source/lacia" ~/.lacia/src
make -C ~/.lacia/src install
```

- 公式の `main` をそのまま使うときは、上のように公式のリポジトリーをクローンします。フォークで運用するときは、フォークを `origin`、公式を `upstream` にします。自分の変更は `origin` へプッシュし、公式の変更は `upstream` から取り込みます。
- `make install` は、ビルドした実行ファイルを `lacia setup` に渡し、`~/.lacia/bin/lacia` に置かせます。`~/.lacia/src` がこのクローンを指していれば、`lacia update` も、このクローンを新しくしてビルドし直します。手元の変更があるときは、取得せずに、いまあるものをビルドします。
- 別の環境で動かすサーバーには、ソースを置きません。コミットしていない変更がない状態で、確定したコミットからビルドした実行ファイルだけを送ります。どのコミットで動いているかは、`lacia serve` が起動のときにログへ書く `version` と、Web の画面のいちばん下の「版」で確かめられます。コミットしていない変更を含めて作った実行ファイルでは、版に「（未コミットの変更あり）」と出ます。
- タスクのために作業ツリーを分けるときは、記録の `local/worktrees/<タスクID>/` に作ります。たとえば、`git worktree add -b <ブランチ> "$HOME/records/local/worktrees/T1001"` のように作ります。`local/` は Git に入りません。取り込みが済んだら、`git worktree remove` で片づけます。

## コマンド

`lacia help` で、すべてのコマンドと引数が出ます。コマンドは、`cmd/lacia/commands.go` の表で定義しています。この表にコマンドを足したら、下の表にも足します。足していないと、テストが知らせます。

| コマンド | すること |
| --- | --- |
| `lacia task list`、`show`、`log` | タスクを読みます |
| `lacia task new`、`set`、`field`、`note`、`attach`、`claim` | タスクを作り、状態を変え、履歴や成果物を足します |
| `lacia task lint` | タスクの文章を点検します |
| `lacia ask` | レイシアへ依頼を送ります |
| `lacia rules` | 会話が従う決まりを出します |
| `lacia setup` | この端末を、レイシアを使える状態にそろえます。はじめは記録を作り、実行ファイルを置き、ログインのときにサーバーが起動するよう登録します。何度実行しても構いません |
| `lacia update`、`lacia uninstall` | `update` で、導入のときと同じ入口をもう1度実行して、新しい版にします。`uninstall` で、`lacia setup` が置いたものを消します。記録は残します |
| `lacia init` | 記録のリポジトリーに、呼び名と、予定を読むアカウントを書きます |
| `lacia codex`、`lacia claude` | 決まりと、きょうの状況を渡して、会話を起動します。記録の外で実行すると、記録の場所へ移って始めます |
| `lacia codex-login` | 1つのサーバーの中で、ログインを分けて Codex を起動します |
| `lacia open`、`lacia root`、`lacia shell` | `open` でブラウザーに Web の画面を開き、`root` で記録の場所を出し、`shell` で記録の場所のシェルを開きます。記録の外からも使えます |
| `lacia calendar list` | サーバーが保存した予定を読みます |
| `lacia docs` | 実行ファイルに入っている設計文書を読みます |
| `lacia serve` | サーバーを動かします |
| `lacia restart`、`lacia busy` | `restart --when-idle` で、処理が終わってからのサーバーの再起動を予約します。`busy` で、処理中の依頼や動いている会話がないかを調べます |
| `lacia config`、`lacia version` | `config` で、サーバーの設定ごとに、いまの値と、その値を決めた場所を読みます。`version` で、版ともとになったコミットを読みます |
| `lacia schedules`、`lacia todo` | 予約の定義を確かめ、`TODO.md` を作り直します |
| `lacia project note` | プロジェクトの履歴に書きます |
| `lacia doctor` | 記録の形と、この端末の導入の状態を点検します。直さずに、誤りと、直すことを出します |
| `lacia notice`、`lacia self` | `notice` で心が動いたことを書き留め、`self` でいまの記録と、次の書き直しに使う書き留めを読みます。`self improve` で、仕組みで直せそうなことの候補を読み、行き先を残します |
| `lacia who` | この会話の名前と、いま動いている会話の一覧を表示します |
| `lacia agent` | サーバーから通知用の情報を読み、登録済みの端末に届く予約と申し送りも受け取ります。macOS の通知アプリも、このコマンドを起動します |
| `lacia peer`、`lacia handoff` | `peer` で相手のプロファイルに伝言を送り、`handoff` でタスクの側へ用件を引き継ぎます |
| `lacia discord` | Discord で依頼を受け付ける人を確かめたり、受け付けをしばらく止めたり、返事に PDF を添えたりできます |
| `lacia wait`、`lacia job run`、`lacia tell` | `wait` で計算の終了を待ちます。`job run` はジョブの投入と待ちの登録をまとめて行い、`tell` は別の会話へ依頼を届けます |
| `lacia run report` | 定期タスクの実行の結果を保存します。定期タスクを受け持つ会話が使います |
| `lacia shims` | 実行ファイルに埋め込んであるコマンドを書き出します |

記録は、定期タスクを領域の下に置く形式2を使います。この版の `lacia` は、形式2の記録だけを扱います。以前の形式の記録は、記録を移すコマンド（`migrate`）が入っている、2026-11-28 より前の版で移します。手順は[当時の移行手順](https://github.com/imos/lacia/blob/1570490e1d8c669f4dd026e08df37963eaf71d59/docs/design/records.md#形を移すlacia-migrate)にあります。

## 設定

`lacia serve` の設定は、コマンドラインの引数か、設定ファイル（`--config`）に書きます。一覧は、[設定の一覧](docs/design/settings.md)にあります。

## 文書

設計の説明は、[docs/design/](docs/design/README.md) にあります。お使いの版では、`lacia docs` で設計文書の一覧、`lacia docs overview` で全体の構成を読めます。Webでは画面下部の「設計の文書」から開けます。はじめは、[全体の構成](docs/design/overview.md)から読んでください。

| 文書 | 中身 |
| --- | --- |
| [全体の構成](docs/design/overview.md) | 部品と、役割の分け方 |
| [記録のリポジトリー](docs/design/records.md) | タスク、定期タスク、覚え書きの置き場所と、記録の設定 |
| [決まりと会話の起動](docs/design/rules.md) | `lacia rules`、`lacia init`、`lacia codex`、`lacia claude` |
| [依頼の受付](docs/design/requests.md) | 依頼が会話へ届くまで |
| [導入と更新](docs/design/install.md) | `lacia setup`、`lacia update`、`lacia uninstall` と、端末の点検 |
| [設定の一覧](docs/design/settings.md) | `lacia serve` の設定と、記録の設定 |
| [開発の決まり](AGENTS.md) | このリポジトリーで作業するときの決まり |

## ライセンス

レイシア本体は、個人が自分の生活や仕事のために利用できます。販売や共有に関する条件は、[ライセンス](LICENSE)をご覧ください。`lacia setup` が新しい記録へ書き出す初期の中身（`internal/starter/files/`）は、この条件によらず、MIT No Attribution（[条文](internal/starter/LICENSE)）で使えます。同梱物や依存ライブラリーの出典と条件は、[第三者の権利表示](THIRD_PARTY_NOTICES.md)にあります。

## コードの場所

| 場所 | 中身 |
| --- | --- |
| `cmd/lacia/` | コマンドの受付、サーバーの起動と各機能の初期設定 |
| `internal/task/` | タスクの読み書き、ID の採番、文章の点検 |
| `internal/web/` | Web の画面と API |
| `internal/request/`、`internal/codex/`、`internal/claudecode/` | 依頼の待ち行列と、Codex や Claude Code とのやり取り |
| `internal/schedule/` | 予約 |
| `internal/starter/` | 記録の初期の中身。ここのファイルだけは MIT No Attribution で使えます |
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
| `tools/metrics/` | テストを除く Go の行数、長い関数、長いファイルを数える道具 |
| `.github/workflows/check.yml` | プッシュとプルリクエストのたびに走る検査 |
| `.github/workflows/release.yml`、`Makefile` の `dist` | 版のタグごとに、検査を通してから実行ファイルとチェックサムの一覧を作り、成果物の証明を付けてリリースにします |
| `install.sh`、`Makefile` の `install` | 導入の入口です。スクリプトは最新のリリースを取得し、`make install` は手元でビルドして、どちらも `lacia setup` を呼びます |
| `docs/install-guide.md`、`docs/images/` | 導入の案内と、その画像です。画像は、合わせて 1 MiB までにします |

## 試し方

```sh
gofmt -l .
go vet ./...
go test ./...
go run golang.org/x/tools/cmd/deadcode@v0.53.0 -test ./...
go run ./tools/metrics -base origin/main
```

GitHub Actions も、プッシュとプルリクエストのたびに、同じ検査を走らせます。`deadcode` は、本体からもテストからも呼ばれない関数を挙げます。何か挙がると、検査は失敗します。最後のコマンドは、`main` からの行数の増減と、100行以上の関数、1,000行以上のファイルを出します。サーバーの起動は、`lacia setup` が書く形の設定と記録の初期の中身と、複数のプロファイルを1つのファイルに書く設定の2通りで、テストが確かめます（`cmd/lacia/serve_start_test.go`）。導入のスクリプトは、テストの中に立てた偽のリリースを相手に動かして確かめます（`install_test.go`）。最初の3つのコマンドは、`make test` でまとめて走らせられます。

`deadcode` の結果は、どの OS 向けのファイルを読むかで変わります。検査は Linux で走るので、macOS で走らせると、Linux でだけ使われる関数が挙がることがあります。2026-11-28 には、`-test` を付けると macOS と Linux の結果は同じでした。`-test` を付けずに測ったときは、`internal/caller` の `parseStat` が macOS でだけ挙がりました。検査と同じ Linux の結果は、道具を手元の OS 向けに入れてから、`GOOS=linux` を付けて走らせると見られます。`go run` に `GOOS=linux` を付けると、道具そのものが Linux 向けに作られて動かないためです。

```sh
go install golang.org/x/tools/cmd/deadcode@v0.53.0
GOOS=linux "$(go env GOPATH)/bin/deadcode" -test ./...
```
