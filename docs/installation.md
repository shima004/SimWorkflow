# SimWorkflow CLIのインストール

[GitHub Releases](https://github.com/shima004/SimWorkflow/releases) から利用するOSとCPUに合うアーカイブと `checksums.txt` をダウンロードします。GoやDockerはCLIの起動には不要です。実験ワーカーはLinuxコンテナ上で動作し、Windows・macOSではクライアントコマンドを利用します。

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

新しいアーカイブを取得し、チェックサムを確認して実行ファイルを置き換えます。共通設定は別ファイルなので引き継がれます。更新後は `simworkflow --version` でバージョンを確認してください。
