# SimWorkflow CLI

Go製のCLIの土台です。Argo Workflows、S3互換サービス、コンテナイメージレジストリの接続設定を環境変数から読み込みます。
接続設定と実験YAMLの読み込み・検証、実行計画の展開、ADFのモジュール設定ファイルの生成ができます。APIへの接続、認証の成否確認、Workflow操作やイメージ転送は今後実装します。

Go 1.24以降が必要です。YAMLの処理には `go.yaml.in/yaml/v3` を使用します。

## 実験定義の設計

実験用YAMLは、エージェントのモジュール候補を `matrix.modules` に指定し、直積で組み合わせを生成する設計です。
`matrix.exclude` で特定の組み合わせを除外し、各構成をマップ・シードごとに実行します。
シミュレータと各エージェントには、それぞれ `resources.cpu`（コア相当の数値）と `resources.memory`（`4Gi` など）を指定します。

- [実験定義YAMLの仕様](docs/experiment-yaml.md)
- [モジュール比較のサンプル](examples/experiments/module-comparison.yaml)（除外後3構成 × マップ1件 × シード3件 = 9回）
- [シミュレーターの起動仕様](docs/simulator.md)（`ghcr.io/shima004/rcrs-server:v1.0.4`）

以下のコマンドはサービスの接続情報なしで利用でき、ネットワーク通信は行いません。

```sh
# 設定と、除外後に実行可能な組み合わせが残ることを検証
go run ./cmd/simworkflow experiment validate -f examples/experiments/module-comparison.yaml

# 展開された構成・マップ・シード・リソースと総実行回数を確認
go run ./cmd/simworkflow experiment plan -f examples/experiments/module-comparison.yaml

# JSONで実行計画を取得
go run ./cmd/simworkflow experiment plan -f examples/experiments/module-comparison.yaml --format json
```

展開する実行数の上限は既定で10,000件です。意図した大規模実験では `--max-runs 20000` などで変更できます。
YAMLからArgo Workflowへの変換と実サービスでの実行は未実装です。

## モジュール設定ファイルの生成

実験YAMLと元設定から、構成ごとに設定ファイルを作成できます。マップやシードが異なっても、同じモジュール構成のファイルは1つだけ作成します。

```sh
# Java版: 3構成の.cfgとmanifest.jsonを生成
go run ./cmd/simworkflow experiment modules \
  -f examples/experiments/module-comparison.yaml \
  --agent comparison \
  --base examples/module-configs/java.cfg \
  --format java --output-dir /tmp/module-configs-java

# Python版: 2構成の.yamlとmanifest.jsonを生成
go run ./cmd/simworkflow experiment modules \
  -f examples/experiments/python-module-comparison.yaml \
  --agent python-comparison \
  --base examples/module-configs/python.yaml \
  --format python --output-dir /tmp/module-configs-python
```

出力ディレクトリは新規のものを指定します。既存のディレクトリは上書きしません。
`manifest.json` には各ファイルと、実行計画に共通する構成ID・エージェント名・イメージ・モジュール設定・リソース設定を記録します。
指定していない元設定は残し、元設定に存在しないキーへの上書きはエラーにします。
同梱の元設定は生成処理を試すための最小例です。実際の実験では自分のエージェントの完全な設定ファイルを `--base` に指定してください。
`my_team` や `src.my_team` の実装は例示用なので、実際にイメージに含めた実装に置き換えます。
生成ファイルをコンテナに渡す手順は以下のテンプレートREADMEに記載しています。

## エージェント用コンテナテンプレート

各ディレクトリのREADMEに従って、Dockerfileと補助ファイルをエージェントリポジトリのルートへコピーして使います。
ビルド・レジストリへの登録方法と、実験用モジュール設定をマウントして起動する例を同梱しています。

- [Java版](templates/agents/java/README.md)
- [Python版（pip / uv）](templates/agents/python/README.md)

テンプレートにはエージェント本体を含みません。Java版・Python uv版のDockerビルドと、生成設定のADFでの読み込みを確認済みです。
実サービス接続と実験の完走は未確認です。[検証記録](docs/validation.md)を参照してください。

## 動作確認

```sh
cp .env.example .env
chmod 600 .env
# .env を編集して接続先と認証情報を設定
go run ./cmd/simworkflow -env-file .env config check
```

`.env.example` の値のままでも設定形式の検証はできます。実サービスを操作する際には実際の接続情報に置き換えてください。
`config check` はネットワーク通信を行わず、設定の不足や形式を検証します。認証情報や接続先の値は表示しません。

本番では環境変数を直接渡します。`.env` は `-env-file` を指定した場合のみ読み込みます。

```sh
go build -o bin/simworkflow ./cmd/simworkflow
# 必須の環境変数を設定したシェルで実行
./bin/simworkflow config check
```

優先順位は **環境変数 > 指定した .env > デフォルト値** です。環境変数を明示的に空に設定した場合も `.env` より優先されます。
`.env` は `KEY=value`、`export KEY=value`、単一引用符・二重引用符、コメントに対応します。値はリテラルとして扱い、変数展開・コマンド実行・エスケープ変換・複数行の値には対応しません。引用符を含む値はもう一方の引用符で囲んでください。
`.env` と `.env.*` はGit管理対象から除外しています（`.env.example` を除く）。

## 設定項目

| 環境変数                  | 必須 | デフォルト / 用途                                                 |
| ------------------------- | ---- | ----------------------------------------------------------------- |
| `ARGO_WORKFLOW_SERVER`    | 必須 | Argo ServerのURL（`https://argo.example.com` など）               |
| `ARGO_WORKFLOW_NAMESPACE` | 任意 | `argo`                                                            |
| `ARGO_WORKFLOW_TOKEN`     | 任意 | Bearer認証用のトークン。`Bearer` 接頭辞なし。未指定は匿名アクセス |
| `S3_ENDPOINT`             | 必須 | S3互換サービスのURL                                               |
| `S3_REGION`               | 任意 | `us-east-1`                                                       |
| `S3_BUCKET`               | 必須 | バケット名                                                        |
| `S3_ACCESS_KEY_ID`        | 必須 | アクセスキーID                                                    |
| `S3_SECRET_ACCESS_KEY`    | 必須 | シークレットアクセスキー                                          |
| `S3_SESSION_TOKEN`        | 任意 | 一時認証情報のセッショントークン                                  |
| `S3_FORCE_PATH_STYLE`     | 任意 | `true`。パス形式のバケットアクセスを使用する設定                  |
| `REGISTRY_URL`            | 必須 | イメージレジストリのURL                                           |
| `REGISTRY_USERNAME`       | 任意 | パスワードと組み合わせて設定                                      |
| `REGISTRY_PASSWORD`       | 任意 | ユーザー名と組み合わせて設定                                      |
| `REGISTRY_TOKEN`          | 任意 | ユーザー名・パスワードと併用不可。`Bearer` 接頭辞なし             |
| `HTTP_TIMEOUT`            | 任意 | `30s`。将来のHTTPリクエスト用。正の期間を指定                     |

URLは `http://` または `https://` が必須です。URL内のユーザー情報、クエリ、フラグメントは受け付けません。
レジストリの認証は匿名・ユーザー名とパスワード・トークンのいずれかを選択できます。
`ARGO_WORKFLOW_NAMESPACE` と `S3_REGION` は省略するとデフォルト値を使い、明示的な空文字はエラーになります。

## 検証と構成

```sh
go test ./...
go vet ./...
```

- `cmd/simworkflow`: CLIのエントリーポイントと `config check`
- `internal/config`: 接続設定の型、環境変数の読み込み・検証、`.env` の読み込み
- `internal/experiment`: 実験YAMLの読み込み・検証と実行計画の展開
- `internal/moduleconfig`: Java／Python版ADFモジュール設定の生成

接続処理の実装時には `config.Load(os.LookupEnv)` が返す設定を使用できます。`.env` を併用する場合は `config.WithFallback` で環境変数の読み込みに重ねます。
