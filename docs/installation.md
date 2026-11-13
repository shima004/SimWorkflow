# SimWorkflow CLIのインストール

[GitHub Releases](https://github.com/shima004/SimWorkflow/releases) から利用するOSとCPUに合うアーカイブと `checksums.txt` をダウンロードします。GoやDockerはCLIの起動には不要です。`agent build` を使用する場合はDocker CLI・BuildxとDockerデーモンが必要です。詳しくは [ビルド・登録ガイド](https://github.com/shima004/SimWorkflow/blob/main/docs/agent-build.md) を参照してください。実験ワーカーはLinuxコンテナ上で動作し、Windows・macOSではクライアントコマンドを利用します。

## 配布物の選択

| OS | 配布ファイルのOS表記 | 形式 |
| --- | --- | --- |
| Linux／WSL | linux | tar.gz |
| macOS | darwin | tar.gz |
| Windows | windows | zip |

CPUはIntel／AMDの64bitなら `amd64`、Apple SiliconなどのARM64なら `arm64` を選びます。

## チェックサムの確認

Linuxでは `sha256sum ダウンロードしたファイル`、macOSでは `shasum -a 256 ダウンロードしたファイル`、WindowsのPowerShellでは `Get-FileHash ダウンロードしたファイル -Algorithm SHA256` を実行し、`checksums.txt` の該当行と一致することを確認してください。

## PATHへの配置

Linux／macOSの例（バージョン・OS・CPUは選択したファイルに合わせて変更）：

```sh
tar -xzf simworkflow_v0.1.0_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 simworkflow "$HOME/.local/bin/simworkflow"
export PATH="$HOME/.local/bin:$PATH"
simworkflow --version
```

PATHの設定は利用するシェルの設定ファイルにも追加してください。WindowsではZIPを展開し、`simworkflow.exe` を配置したフォルダーをユーザーのPATHに追加して、新しいターミナルを開きます。

配布バイナリはコード署名・公証をしていません。OSの設定によって初回起動時に確認が必要になる場合があります。

## 接続設定

初回だけ接続設定を作成します。

```sh
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlに接続先・名前空間・バケット名を入力
simworkflow credentials set s3
# Argo・レジストリに認証が必要なら、それぞれ登録
simworkflow config check
```

接続設定と認証情報は利用者ごとに保存されるため、以後はどのディレクトリからでも実行できます。新規設定ではOSの資格情報ストアを使用し、認証情報はYAMLへ書きません。SimScopeは任意機能です。

Argoの認証には `simworkflow credentials set argo`、レジストリのユーザー名・パスワードには `simworkflow credentials set registry`、トークンには `simworkflow credentials set registry --auth token` を使います。OSストアを使わないCI向けの方式やLinux／WSLの要件は [認証情報ガイド](https://github.com/shima004/SimWorkflow/blob/main/docs/credentials.md) を参照してください。

接続設定の詳しい書式は [接続設定ガイド](https://github.com/shima004/SimWorkflow/blob/main/docs/configuration.md) を参照してください。

## 更新

自己更新対応の配布バイナリでは、次のコマンドを使えます。

```sh
# 更新候補だけを確認する（ファイルは変更しない）
simworkflow self-update --check --prerelease

# v0.xなどのプレリリースを含めて更新する
simworkflow self-update --prerelease

# 特定の新しいバージョンへ更新する
simworkflow self-update --version v0.1.2

simworkflow --version
```

`--prerelease` を省略すると正式リリースだけを探します。このプロジェクトでは `v0.x` をプレリリースとして公開するため、v0.xを使う場合はこのオプションを付けます。`--version` は公開済みのタグへ置き換えて指定してください。プレリリースも対象にできます。古いバージョンへのダウングレードは行わず、同じバージョンなら置き換えを省略します。

GitHub ReleasesからOS・CPUに合うアーカイブと `checksums.txt` を取得し、SHA-256と新しいCLIの起動を確認してから置き換えます。ダウンロード・検証・起動確認が失敗した場合は、インストール済みCLIを変更しません。置き換え後に復旧が必要になった場合は、表示されたバックアップの案内に従ってください。

更新に接続設定やGitHubトークンは不要です。設定・認証情報はそのまま使えます。インストール先への書き込み権限が必要で、自動で管理者権限へ昇格しません。

通常の `go run`・開発ビルド（バージョンが `dev`）では確認だけが可能で、実ファイルの更新は拒否します。このコマンドを搭載していない旧CLIでは、新しいアーカイブを手動で取得・チェックサム確認して、最初の1回だけ実行ファイルを置き換えてください。その後は自己更新を使えます。

更新によってCLIに埋め込まれた既定helperも切り替わります。設定やオプションでのhelper上書きは引き続き優先します。送信済みの実験は変更しません。

## 更新できない場合

| 症状 | 対処 |
| --- | --- |
| 更新候補が見つからない | プレリリースを使う場合は `--prerelease` を付ける。ネットワークからGitHub Releasesへ接続できるか確認する |
| インストール先へ書き込めない | 自分が書き込めるディレクトリへ配置するか、手動でアーカイブを展開して置き換える |
| 更新のロックが残っている | 更新プロセスが動いていないことを確認し、実行ファイル名に `.update.lock` を付けたファイルを取り除く |
| Windowsで実行ファイルを置き換えられない | WSL共有フォルダーなどのUNCパスを避け、ローカルドライブへ配置する。更新後、削除できなかった旧ファイルが隠しファイルとして残る場合がある |
| バージョンが `dev` で更新を拒否される | 開発ビルドは確認のみ対応。[GitHub Releases](https://github.com/shima004/SimWorkflow/releases) の配布バイナリを手動で導入する |

インストール後は [接続設定](https://github.com/shima004/SimWorkflow/blob/main/docs/configuration.md) → [最初の実験](https://github.com/shima004/SimWorkflow/blob/main/docs/quickstart.md) の順に進めてください。
