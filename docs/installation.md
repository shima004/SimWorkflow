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

# 現在のv0.xプレリリースを含めて更新する
simworkflow self-update --prerelease

# 特定の新しいバージョンへ更新する
simworkflow self-update --version v0.1.2

simworkflow --version
```

`--prerelease` を省略すると正式リリースだけを探します。現在の `v0.x` はGitHub上でプレリリースとして公開する設定なので、通常はこのオプションを付けます。`--version` は指定した公開済みバージョンを対象にし、プレリリースでも指定できます。古いバージョンへのダウングレードは行いません。同じバージョンなら置き換えを省略します。

GitHub Releasesの `shima004/SimWorkflow` からOS・CPUに合うアーカイブと `checksums.txt` を取得し、SHA-256と新しいCLIの `--version` 起動を確認してから置き換えます。ダウンロード・検証・起動確認が失敗した場合は、インストール済みCLIを変更しません。置き換えに失敗した場合はライブラリが復旧を試み、復旧にも失敗した場合はバックアップの場所を表示します。Windowsでは実行中の旧ファイルを別名へ移して置き換え、実行中で削除できない旧ファイルは隠しファイルとして残る場合があります。Windows向けCLIはローカルドライブへ配置してください。WSL共有フォルダ（UNCパス）上では、実行中ファイルの置き換えを拒否されることを確認しています。

設定ファイルやOSの資格情報ストアを読み込まず、設定・認証情報はそのまま引き継ぎます。GitHubトークンも使用せず、公開Releaseへアクセスします。インストール先のディレクトリへの書き込み権限が必要です。自動で管理者権限へ昇格しません。同時更新を防ぐ、実行ファイル名に `.update.lock` を付けたロックファイルが残っている場合は、更新プロセスが動いていないことを確認してから取り除いてください。

通常の `go run`・開発ビルド（バージョンが `dev`）では確認だけが可能で、実ファイルの更新は拒否します。このコマンドを搭載していない旧CLIでは、新しいアーカイブを手動で取得・チェックサム確認して、最初の1回だけ実行ファイルを置き換えてください。その後は自己更新を使えます。

更新によってCLIに埋め込まれた既定helperも切り替わります。設定やオプションでのhelper上書きは引き続き優先します。送信済みの実験は変更しません。
