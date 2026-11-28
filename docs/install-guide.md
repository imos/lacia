# レイシアの導入の案内

Mac にレイシアを入れて、使いはじめるまでの手順です。終わると、Mac にログインしているあいだ、わたしのサーバーが動きつづけます。ブラウザーで開くホームの画面から、わたしに依頼できます。タスクと記録は、ご自分の GitHub の非公開のリポジトリーに保存されます。

手順は、次の順に進みます。

1. [Codex か Claude Code を入れる](#codex-か-claude-code-を入れる)
2. [記録のリポジトリーを作る](#記録のリポジトリーを作る)
3. [GitHub へつなぐ](#github-へつなぐ)
4. [スクリプトで入れる](#スクリプトで入れる)か、[クローンして入れる](#クローンして入れる)

仕組みの詳しい説明は、設計の文書の[導入と更新](design/install.md)にあります。

## 始める前に

要るものは、次の3つです。

- Mac。導入のスクリプトが使えるのは、Apple シリコン（M1 以降）の Mac です。Intel の Mac と Linux では、[クローンして入れる](#クローンして入れる)を使います。Linux では、ログインのときにサーバーを起動する登録は行わず、起動のしかたを表示します。
- GitHub のアカウント
- ターミナル。コマンドは、ターミナルに貼り付けて実行します。

管理者の権限は使いません。導入で置くファイルは、ホームの下だけです。道具は `~/.lacia` にまとまり、ほかに `~/.local/bin/lacia` のリンク、ログインのときにサーバーを起動する登録（`~/Library/LaunchAgents/jp.imoz.lacia.serve.plist`）、記録のフォルダーを置きます。通知アプリを入れたときは、`~/Applications` にアプリが加わります。

## Codex か Claude Code を入れる

わたしは、依頼を Codex か Claude Code の会話に渡して動きます。どちらかを入れて、ターミナルで1度起動し、ログインを済ませておきます。

- Codex: <https://github.com/openai/codex>
- Claude Code: <https://claude.com/claude-code>

両方が入っているときは、依頼は Codex で受け、夜の処理は Claude Code で動かします（[夜の処理](#夜の処理)）。

## 記録のリポジトリーを作る

GitHub の <https://github.com/new> で、新しいリポジトリーを作ります。

- 名前は、たとえば `<GitHub のユーザー名>-lacia` にします。Mac の記録のフォルダーは、既定でこの名前になります（`~/<リポジトリーの名前>`）。
- 公開の範囲は、**Private** を選びます。記録には、予定や仕事の内容が入ります。公開のリポジトリーを指定すると、`lacia setup` は止まります。
- README、`.gitignore`、ライセンスは、どれも足しません。中身が空のリポジトリーにすると、`lacia setup` が記録の初期の中身を置きます。

作ったら、画面に出る HTTPS の URL（`https://github.com/<名前>/<リポジトリー>.git`）を控えます。

## GitHub へつなぐ

わたしのサーバーは、記録の変更を自分でコミットして、GitHub へプッシュします。ログインのときに起動したサーバーもプッシュできるように、GitHub の道具（`gh`）でログインします。

1. `gh` を入れます。Homebrew を使っている Mac では `brew install gh` で入ります。使っていなければ、<https://cli.github.com/> から入れます。
2. ターミナルで、次の2つを実行します。

   ```sh
   gh auth login
   gh auth setup-git
   ```

   `gh auth login` では、`GitHub.com`、`HTTPS` の順に選び、Git の認証にも使うかを尋ねられたら `Yes` を選びます。そのあと、ブラウザーでログインします。`gh auth setup-git` は、Git が GitHub へプッシュするときに、このログインを使うように設定します。

記録のリポジトリーは、HTTPS の URL で指定します。SSH の URL でクローンした記録は、`git -C <記録のフォルダー> remote set-url origin https://github.com/<名前>/<リポジトリー>.git` で HTTPS に変えられます。

## スクリプトで入れる

Apple シリコンの Mac では、ターミナルで次の1行を実行します。Go も `git` も要りません。

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/imos/lacia/main/install.sh | sh
```

中身を読んでから実行したいときは、次のように分けます。

```sh
curl --proto '=https' --tlsv1.2 -fsSLO https://raw.githubusercontent.com/imos/lacia/main/install.sh
less install.sh
sh install.sh
```

スクリプトは、最新のリリースから、この Mac 向けの実行ファイルと、チェックサムの一覧（`SHA256SUMS`）を取得します。実行ファイルが一覧と合えば、その実行ファイルの `lacia setup` を呼びます。合わなければ、何も入れずに止まります。

`lacia setup` は、次の3つを尋ねます。

1. 記録のリポジトリーの URL。[記録のリポジトリーを作る](#記録のリポジトリーを作る)で控えたものです。
2. 記録を置く場所。Enter で、既定の `~/<リポジトリーの名前>` になります。`~/Documents`、`~/Desktop`、`~/Downloads`、iCloud Drive の下は選べません。macOS が、これらの場所に触るプログラムに許可を求めるためです。
3. わたしがお呼びする名前（たとえば「田中さん」）。

続けて、リポジトリーが非公開で空であることを確かめてから、記録の初期の中身を置いてプッシュします。ログインのときにサーバーを起動するよう登録して、ブラウザーでホームの画面（`http://127.0.0.1:10148/`）を開きます。

質問に答える代わりに、引数で渡すこともできます。`sh -s --` のあとに書いた引数は、`lacia setup` に渡ります。

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/imos/lacia/main/install.sh | sh -s -- --repo https://github.com/<名前>/<リポジトリー>.git --name 田中さん
```

使える引数は、`lacia setup --help` か、設計の文書の[導入と更新](design/install.md#全体の流れ)にあります。

## クローンして入れる

本体のソースを手元に置き、手元の Go でビルドして入れる方法です。Intel の Mac と Linux では、こちらを使います。

要るものは、Go と `git` です。Go は <https://go.dev/dl/> から入れます。`git` がない Mac では、`xcode-select --install` で Xcode のコマンドラインツールを入れると、`git` と `make` が入ります。

```sh
git clone https://github.com/imos/lacia.git ~/.lacia/src
make -C ~/.lacia/src install
```

`make install` は、ビルドした実行ファイルの `lacia setup` を呼びます。そこから先は、[スクリプトで入れる](#スクリプトで入れる)と同じです。`lacia setup` へ引数を渡すときは、`SETUP` に書きます。

```sh
make -C ~/.lacia/src install SETUP="--repo https://github.com/<名前>/<リポジトリー>.git"
```

Go は、本体の `go.mod` の `go` の行にある版以降が要ります。`toolchain` の行には、リリースを作る Go の版が書いてあります。手元の Go のほうが古ければ、Go の既定の設定では、ビルドの前にその版を自動で取得します。

## 組織の OAuth クライアントを使う

予定の連携（Google Calendar）に、組織が用意した OAuth クライアントを使うときは、導入のコマンドの前に、2つの環境変数を設定します。値は、組織の案内に書かれています。

```sh
export LACIA_GOOGLE_CLIENT_ID='（組織の案内にある ID）'
export LACIA_GOOGLE_CLIENT_SECRET='（組織の案内にあるシークレット）'
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/imos/lacia/main/install.sh | sh
```

クローンして入れるときも、同じ2行のあとに `make -C ~/.lacia/src install` を実行します。

`lacia setup` は、受け取った値を `~/.lacia/config/google-client.json` に、持ち主だけが読める権限で書きます。記録のリポジトリーには入りません。予定を読むアカウントは、導入のあとで、記録のフォルダーで `lacia init` を実行して設定します。

## 導入のあと

- ホームの画面は `http://127.0.0.1:10148/` です。ブラウザーのブックマークに入れておくと、ターミナルを使わずに開けます。ターミナルからは `lacia open` で開けます。
- 会話を始めるときは、`lacia codex` か `lacia claude` を実行します。記録の外で実行しても、記録のフォルダーへ移って始まります。
- `lacia` のコマンドは、`~/.local/bin/lacia` にあります。見つからないときは、[lacia が見つからない](#lacia-が見つからない)を読んでください。
- Mac の通知を受け取りたいときは、`lacia setup --notifier` で通知アプリを入れます。ビルドに、Xcode のコマンドラインツールが要ります。

## 夜の処理

わたしは、毎日午前4時に、その日の出来事を整理して、「わたしのこと」（`memory/self.md`。わたしが自分について書いている文書）を書き直します。時刻は、記録の `timezone` の設定に従います。既定は日本時間です。

- Claude Code が入っていれば Claude Code で、なければ Codex で動きます。
- 送る内容は、その日のやり取り（最長72時間分）と、記録から読んだファイルです。送り先は、Claude Code なら Anthropic、Codex なら OpenAI です。依頼を Codex で受け、夜の処理を Claude Code で動かす Mac では、ふだんの会話とは別の送り先（Anthropic）へも送られます。
- 送り先を会話と同じ Codex にそろえるときは `self-agent = codex`、やり取りを送らないときは `self-exchanges = false`、夜の処理を止めるときは `self-at = off` を、記録の `.lacia/serve.conf` に書きます。書いたら、`lacia restart --when-idle` でサーバーを起動し直します。

いまの設定で、夜の処理がどちらで動き、何をどこへ送るかは、`lacia config` で確かめられます。`lacia setup` も、終わりに同じ説明を表示します。

## 新しい版にする

```sh
lacia update
```

導入したときと同じ経路で、新しい版を入れます。スクリプトで入れた Mac では最新のリリースを取得し、クローンして入れた Mac では `~/.lacia/src` を新しくしてビルドします。サーバーは、処理中の依頼を終えてから、新しい版で起動し直します。

## やめるとき

```sh
lacia uninstall
```

消すものの一覧を表示して、`yes` の入力を待ちます。消すのは、ログインのときにサーバーを起動する登録、通知アプリ、`~/.local/bin/lacia` のリンク、`~/.lacia` です。記録のフォルダーと、GitHub のリポジトリーは残ります。

- 記録も要らなければ、記録のフォルダーを消し、GitHub のリポジトリーを、リポジトリーの Settings の「Delete this repository」で消します。
- `~/.zshrc` に `~/.local/bin` の行を足していたら、その行も消します。
- GitHub の道具のログインも外すときは、`gh auth logout` を実行します。

## 困ったとき

まず、ターミナルで `lacia doctor` を実行します。この Mac で直すことと、直すためのコマンドを表示します。サーバーが見つけたことは、ホームの画面のいちばん上にも「この端末で直すこと」として出ます。

### lacia が見つからない

`lacia` は `~/.local/bin` にあります。この場所がシェルの `PATH` に入っていないと、コマンドが見つかりません。次の行を `~/.zshrc` に足して、ターミナルを開き直します。

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
```

### サーバーが動いていない

ログ（`~/.lacia/logs/serve.log`）に、止まった理由が書かれています。直したら、`lacia setup` を実行します。ログインのときにサーバーを起動する登録がなければ、登録し直します。

### 依頼を受け付けられない

ログインのときに起動したサーバーは、シェルの設定を読みません。そのため、ターミナルでは動く `codex` や `claude` が、サーバーからは見つからないことがあります。`codex` か `claude` を実行できるターミナルで `lacia setup` を実行すると、そのターミナルの `PATH` をサーバーの登録に書き直します。

### この Mac 向けのリリースがない

導入のスクリプトが「この端末（…）向けのリリースはありません」と表示したときは、[クローンして入れる](#クローンして入れる)を使います。
