# 接続設定

ユーザー共通の `config.yaml` を設定すると、どのディレクトリから実行しても同じ設定を使用できます。環境変数と開発用の `.env` も利用できます。

## 初期設定

```sh
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlを編集して接続先・認証情報を入力
simworkflow config check
```

ソースから実行する場合は `simworkflow` の代わりに `go run ./cmd/simworkflow` を使用します。`config init` は既存ファイルを上書きしません。Unix系OSではディレクトリを0700、ファイルを0600で作成します。初期テンプレートの認証情報は空です。`config path` は読み込むYAMLのパスだけを表示し、認証情報は表示しません。

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

`argoWorkflow`・`s3`・`registry` は既存の `ARGO_WORKFLOW_*`・`S3_*`・`REGISTRY_*` と同じ設定です。`simScope.url` は `SIMSCOPE_URL`、`httpTimeout` は `HTTP_TIMEOUT` に対応します。`simScope.url` は任意設定で、利用するSimScopeのURLを入力してください。未設定の場合は `w` によるブラウザ表示時に設定方法を案内します。全項目の対応と必須条件は、このページの設定項目一覧を参照してください。
テンプレートには名前空間として `argo-workflows` を記載しています。接続先の名前空間へ変更してください。

未知の項目・重複キー・型の不一致・複数のYAML文書はエラーになります。エラーに認証情報の値は含めません。設定ファイルは1MiBまでです。

## 設定項目一覧

以下の必須条件はサービスを使うコマンドに適用します。`--version`、`config init`・`path`、オフラインの実験検証・計画・モジュール生成では接続設定は不要です。YAMLでは前項の対応する項目へ指定します。表のデフォルト値は、設定を省略した場合の値です。

| YAMLの項目 | 環境変数 | 必須 | デフォルト・用途 |
| --- | --- | --- | --- |
| `argoWorkflow.server` | `ARGO_WORKFLOW_SERVER` | 必須 | Argo ServerのURL |
| `argoWorkflow.namespace` | `ARGO_WORKFLOW_NAMESPACE` | 任意 | `argo`。操作する名前空間 |
| `argoWorkflow.token` | `ARGO_WORKFLOW_TOKEN` | 任意 | Bearerトークン。接頭辞なし。未設定は匿名 |
| `s3.endpoint` | `S3_ENDPOINT` | 必須 | S3互換サービスのURL |
| `s3.region` | `S3_REGION` | 任意 | `us-east-1` |
| `s3.bucket` | `S3_BUCKET` | 必須 | 保存先バケット名 |
| `s3.accessKeyID` | `S3_ACCESS_KEY_ID` | 必須 | アクセスキーID |
| `s3.secretAccessKey` | `S3_SECRET_ACCESS_KEY` | 必須 | シークレットアクセスキー |
| `s3.sessionToken` | `S3_SESSION_TOKEN` | 任意 | 一時認証情報のセッショントークン |
| `s3.forcePathStyle` | `S3_FORCE_PATH_STYLE` | 任意 | `true`。パス形式のバケットアクセス |
| `registry.url` | `REGISTRY_URL` | 必須 | イメージレジストリのURL |
| `registry.username` | `REGISTRY_USERNAME` | 任意 | パスワードと組み合わせて指定 |
| `registry.password` | `REGISTRY_PASSWORD` | 任意 | ユーザー名と組み合わせて指定 |
| `registry.token` | `REGISTRY_TOKEN` | 任意 | Bearerトークン。ユーザー名・パスワードと併用不可 |
| `simScope.url` | `SIMSCOPE_URL` | 任意 | デフォルトなし。SimScopeを利用する場合だけ指定 |
| `httpTimeout` | `HTTP_TIMEOUT` | 任意 | `30s`。各HTTPリクエストの制限時間。正の期間 |

URLは `http://` または `https://` が必須です。URL内のユーザー情報、クエリ、フラグメントは受け付けません。
レジストリの認証は匿名・ユーザー名とパスワード・トークンのいずれかを選択できます。
`ARGO_WORKFLOW_NAMESPACE` と `S3_REGION` は省略するとデフォルト値を使い、明示的な空文字はエラーになります。

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

## 開発用の.env

リポジトリのルートで、サンプルをコピーして実際の接続情報を入力します。

```sh
cp .env.example .env
chmod 600 .env
simworkflow config check
```

`.env` は `KEY=value`、`export KEY=value`、単一引用符・二重引用符、コメントに対応します。値はリテラルで、変数展開・コマンド実行・エスケープ変換・複数行の値には対応しません。引用符を含む値はもう一方の引用符で囲んでください。`.env` と `.env.*` はGit管理から除外しています（`.env.example` を除く）。

## 接続確認

`config check` は通信せずに設定の不足・形式を検証します。`config check --connect` はArgo、S3、レジストリへの読み取りアクセスを確認します。どちらも認証情報の値を表示しません。確認する権限やエラーの対処は [サービスへの接続確認](connections.md) を参照してください。

[ドキュメント一覧へ](README.md)
