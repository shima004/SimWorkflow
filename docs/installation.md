# SimWorkflow CLIのインストール

[GitHub Releases](https://github.com/shima004/SimWorkflow/releases) から利用するOSとCPUに合うアーカイブと `checksums.txt` をダウンロードします。GoやDockerはCLIの起動には不要です。実験ワーカーはLinuxコンテナ上で動作し、Windows・macOSではクライアントコマンドを利用します。

| OS | 配布ファイルのOS表記 | 形式 |
| --- | --- | --- |
| Linux／WSL | linux | tar.gz |
| macOS | darwin | tar.gz |
| Windows | windows | zip |

CPUはIntel／AMDの64bitなら `amd64`、Apple SiliconなどのARM64なら `arm64` を選びます。

Linuxでは `sha256sum ダウンロードしたファイル`、macOSでは `shasum -a 256 ダウンロードしたファイル`、WindowsのPowerShellでは `Get-FileHash ダウンロードしたファイル -Algorithm SHA256` を実行し、`checksums.txt` の該当行と一致することを確認してください。

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

初回だけ接続設定を作成します。

```sh
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlに接続先・認証情報を入力
simworkflow config check
```

設定は利用者ごとに保存されるため、以後はどのディレクトリからでも実行できます。環境変数 > カレントディレクトリの `.env` > 共通YAMLの順で設定を適用します。SimScopeは任意機能です。

接続設定の詳しい書式はリポジトリの `docs/configuration.md` を参照してください。更新時は新しいアーカイブの実行ファイルへ置き換えます。接続設定は引き継がれます。
