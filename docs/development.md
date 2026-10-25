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
python3 -m unittest discover -s scripts -p 'test_*.py'
```

テストにはローカルHTTPサーバーを使用するものがあります。実サービスの認証情報は不要です。ワーカーはLinuxコンテナ向けで、macOS・Windows向け配布物はクライアント機能を対象にしています。

Release用の全アーカイブをローカルで作る場合は、GoとPython 3を用意します。出力先は空のディレクトリを指定してください。

```sh
python3 scripts/build-release.py --version v0.1.0 \
  --helper-image "registry.example.com/simworkflow/worker@sha256:実際の64桁のdigest" \
  --output /tmp/simworkflow-release-v0.1.0
```

`--helper-image` は検証済みの実際のdigest参照に置き換えます。ローカル検証で省略した場合はソース内の既定値を使います。正式なReleaseは `helper-image.yml` で同じコミットのLinuxワーカーを公開・確認し、そのdigestを指定してビルドします。macOS・Windowsランナーでクライアントコマンドの起動も確認します。

OSストアへの実際の保存・取得・削除は明示的に選んだときだけ検証できます。仮の一時エントリを作成し、テスト後に削除します。OSストアが利用できる対話環境で実行してください。

```sh
SIMWORKFLOW_TEST_OS_KEYRING=1 go test ./internal/credentials -run TestSystemStoreRoundTrip -v
```

通常のテストはOSストアへアクセスしません。自己更新のテストも実際のインストールを変更せず、一時ディレクトリにコピーしたテスト用実行ファイルだけを更新します。自己更新には [go-selfupdate](https://github.com/creativeprojects/go-selfupdate) を使用し、macOS・WindowsのCIでも実行中の使い捨てバイナリを置き換えるテストを実行します。

## 実装の構成

| パス | 担当 |
| --- | --- |
| `cmd/simworkflow/` | 設定・実験・マップのCLI、TUI、ダウンロード、ワーカーの入口 |
| `internal/cliupdate/` | 公開Releaseの選択・SHA-256検証・自己更新・起動確認 |
| `internal/agentbuild/` | Dockerビルド・単一イメージの登録、レジストリ認証・digest検証 |
| `internal/credentials/` | エンドポイント別のOS資格情報ストア、認証情報の解決 |
| `internal/config/` | 環境変数・共通YAML・.envの重ね合わせと検証 |
| `internal/connections/` | 認証付きのサービス接続確認 |
| `internal/experiment/` | 実験YAMLの検証、組み合わせ・マップ・シードの展開 |
| `internal/moduleconfig/` | Java／Pythonのモジュール設定生成 |
| `internal/maparchive/`・`internal/mapcatalog/` | マップの検査・登録・バージョン解決 |
| `internal/workflow/`・`internal/argo/` | Workflow生成・送信・状態参照 |
| `internal/worker/` | マップ準備、事前計算、本実行、結果収集 |
| `internal/storage/`・`internal/artifacts/` | S3保存・転送、結果の選択・取得 |
| `templates/agents/` | Java・Pythonエージェントのコンテナテンプレート |
| `examples/experiments/` | 実験定義のサンプル |

## ドキュメントを更新する

導入の入口は [README](../README.md)、目的別の案内は [ドキュメント一覧](README.md) にまとめます。設定項目は [接続設定](configuration.md)、実験項目は [実験YAML](experiment-yaml.md) を更新し、重複する仕様説明を増やさないようにします。個人的な実環境の検証記録は `examples/validation/`・`docs/validation.md` にローカルで保存します。これらはGit・Dockerの対象から除外し、公開ドキュメントや自動テストから参照しません。CIでもこれらが追跡されていないことを確認し、含まれている場合はイメージ・Releaseの公開を停止します。公開用のサンプルやテストデータには架空の接続先を使います。

[ドキュメント一覧へ](README.md)
