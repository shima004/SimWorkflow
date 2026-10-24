# 接続設定

ユーザー共通の `config.yaml` を設定すると、どのディレクトリから実行しても同じ設定を使用できます。環境変数と開発用の `.env` も利用できます。

## 初期設定

```sh
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlを編集して接続先・認証情報を入力
simworkflow config check
```

ソースから実行する場合は `simworkflow` の代わりに `go run ./cmd/simworkflow` を使用します。`config init` はディレクトリを0700、ファイルを0600で作成し、既存ファイルは上書きしません。初期テンプレートの認証情報は空です。`config path` は読み込むYAMLのパスだけを表示し、認証情報は表示しません。

`XDG_CONFIG_HOME` が設定されていれば、その下の `simworkflow/config.yaml` を使用します。絶対パスを指定してください。未設定時はOSのユーザー設定ディレクトリを使います。

| 環境 | 通常の保存先 |
| --- | --- |
| Linux／WSL | `~/.config/simworkflow/config.yaml` |
| macOS | `~/Library/Application Support/simworkflow/config.yaml` |
| Windows | `%AppData%\simworkflow\config.yaml` |

ファイルが存在しない場合、自動読み込みは省略します。環境変数だけでの運用もできます。

## YAMLの書式

`config init` が作成するテンプレートは [config.example.yaml](../internal/config/config.example.yaml) です。既存の `.env` の値を対応する項目へ移せます。

```yaml
argoWorkflow:
  server: https://argo.example.com
  namespace: argo-workflows
  token: ""
s3:
  endpoint: https://s3.example.com
  region: us-east-1
  bucket: simworkflows
  accessKeyID: ""
  secretAccessKey: ""
  sessionToken: ""
  forcePathStyle: true
registry:
  url: https://ghcr.io
  username: ""
  password: ""
  token: ""
simScope:
  url: ""
httpTimeout: 30s
```

`argoWorkflow`・`s3`・`registry` は既存の `ARGO_WORKFLOW_*`・`S3_*`・`REGISTRY_*` と同じ設定です。`simScope.url` は `SIMSCOPE_URL`、`httpTimeout` は `HTTP_TIMEOUT` に対応します。`simScope.url` は任意設定で、利用するSimScopeのURLを入力してください。未設定の場合は `w` によるブラウザ表示時に設定方法を案内します。環境変数名と必須項目は [READMEの設定項目](../README.md#設定項目) を参照してください。
未知の項目・重複キー・型の不一致・複数のYAML文書はエラーになります。エラーに認証情報の値は含めません。設定ファイルは1MiBまでです。

## 優先順位と別ファイルの指定

優先順位は **環境変数 > `.env` > `config.yaml` > デフォルト値** です。値ごとに重ね合わせ、環境変数を明示的に空にした場合も優先します。YAMLの `""`・`false` も明示された値として扱い、省略した項目だけが下位のデフォルト値を使用します。

```sh
# 共通設定とは別のYAMLを使う
simworkflow --config /path/to/config.yaml experiment submit -f experiment.yaml

# 別の場所へテンプレートを作る
simworkflow --config ./config.local.yaml config init

# .envを読まず、環境変数と共通YAMLを使う
simworkflow --env-file='' experiment status WORKFLOW_NAME

# 設定ファイルを一切読まず、環境変数だけで使う
simworkflow --config='' --env-file='' config check
```

`--config FILE` を明示した場合、そのファイルが存在しなければエラーです。共通YAMLは同時に読み込みません。`--config=''` はYAML読み込みだけを無効にします。これらの全体オプションは `experiment`・`map`・`config` より前に置きます。

`.env` はカレントディレクトリから自動で読みます。`--env-file FILE` で別ファイルを選択でき、`--env-file=''` でdotenv読み込みだけを無効にできます。設定値はシェル式や環境変数として展開しません。

認証情報を入力した設定ファイルはGitへ追加しないでください。設定値はバイナリや実験の結果バンドルに埋め込みません。オフラインの `experiment validate`・`plan`・`modules` は接続設定を読みません。
