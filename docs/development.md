# 開発ガイド

利用者向けバイナリの導入は [インストール](installation.md)、公開に使うタグとActionsは [リリース手順](distribution.md#cliをリリースする) を参照してください。このページのコマンドはリポジトリのルートから実行します。

## ソースから実行・ビルドする

Go 1.24以降が必要です。CIではGo 1.26を使用します。

```sh
go run ./cmd/simworkflow --version
go run ./cmd/simworkflow experiment validate -f examples/experiments/module-comparison.yaml
go build -o bin/simworkflow ./cmd/simworkflow
./bin/simworkflow --version
```

Windowsでは出力名を `bin/simworkflow.exe` にします。通常のビルドのバージョン表示は `dev` です。Release用のビルドはタグ名とソースコミットを埋め込みます。

ユーザー向けガイドの `simworkflow` は、ソースから試す場合は `go run ./cmd/simworkflow` に置き換えられます。`go run` ではリポジトリのルートから実行してください。共通YAMLや `.env` の扱いは [接続設定](configuration.md) と同じです。

## 検証する

```sh
go test -race ./...
go vet ./...
go mod verify
```

テストにはローカルHTTPサーバーを使用するものがあります。実サービスの認証情報は不要です。ワーカーはLinuxコンテナ向けで、macOS・Windows向け配布物はクライアント機能を対象にしています。

Release用の全アーカイブをローカルで作る場合は、GoとPython 3を用意します。出力先は空のディレクトリを指定してください。

```sh
python3 scripts/build-release.py --version v0.1.0 --output /tmp/simworkflow-release-v0.1.0
```

`release.yml` は全体テストと6環境向けビルドに加え、macOS・Windowsランナーでクライアントコマンドの起動を確認します。`helper-image.yml` はLinuxワーカーイメージを検証・公開します。

OSストアへの実際の保存・取得・削除は明示的に選んだときだけ検証できます。仮の一時エントリを作成し、テスト後に削除します。OSストアが利用できる対話環境で実行してください。

```sh
SIMWORKFLOW_TEST_OS_KEYRING=1 go test ./internal/credentials -run TestSystemStoreRoundTrip -v
```

通常のテストはOSストアへアクセスしません。

## 実装の構成

| パス | 担当 |
| --- | --- |
| `cmd/simworkflow/` | 設定・実験・マップのCLI、TUI、ダウンロード、ワーカーの入口 |
| `internal/credentials/` | エンドポイント別のOS資格情報ストア、認証情報の解決 |
| `internal/config/` | 環境変数・共通YAML・.envの重ね合わせと検証 |
| `internal/connections/` | 認証付きのサービス接続確認 |
| `internal/experiment/` | 実験YAMLの検証、組み合わせ・マップ・シードの展開 |
| `internal/moduleconfig/` | Java／Pythonのモジュール設定生成 |
| `internal/maparchive/`・`internal/mapcatalog/` | マップの検査・登録・版解決 |
| `internal/workflow/`・`internal/argo/` | Workflow生成・送信・状態参照 |
| `internal/worker/` | マップ準備、事前計算、本実行、結果収集 |
| `internal/storage/`・`internal/artifacts/` | S3保存・転送、結果の選択・取得 |
| `templates/agents/` | Java・Pythonエージェントのコンテナテンプレート |
| `examples/experiments/` | 実験定義のサンプル |
| `examples/validation/` | 特定環境での検証記録 |

## ドキュメントを更新する

導入の入口は [README](../README.md)、目的別の案内は [ドキュメント一覧](README.md) にまとめます。設定項目は [接続設定](configuration.md)、実験項目は [実験YAML](experiment-yaml.md) を更新し、重複する仕様説明を増やさないようにします。実環境での確認結果と未確認範囲は [検証記録](validation.md) へ記載します。

[ドキュメント一覧へ](README.md)
