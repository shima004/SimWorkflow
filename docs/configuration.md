# 接続設定

接続先・名前空間・バケット名などはユーザー共通の `config.yaml` に指定します。認証情報は [OSの資格情報ストア](credentials.md) へ保存します。CIでは環境変数から渡すこともできます。

## 初期設定

```sh
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlを編集して接続先・名前空間・バケット名を入力
simworkflow credentials set s3
# Argo・レジストリに認証が必要なら、それぞれ登録
simworkflow config check
```

ソースから実行する場合は `simworkflow` の代わりに `go run ./cmd/simworkflow` を使用します。`config init` は既存ファイルを上書きしません。Unix系OSではディレクトリを0700、ファイルを0600で作成します。初期テンプレートは `credentials.store: keyring` を選択し、認証情報の項目を含みません。`config path` は読み込むYAMLのパスだけを表示し、認証情報は表示しません。

`XDG_CONFIG_HOME` が設定されていれば、その下の `simworkflow/config.yaml` を使用します。絶対パスを指定してください。未設定時はOSのユーザー設定ディレクトリを使います。

| 環境 | 通常の保存先 |
| --- | --- |
| Linux／WSL | `~/.config/simworkflow/config.yaml` |
| macOS | `~/Library/Application Support/simworkflow/config.yaml` |
| Windows | `%AppData%\simworkflow\config.yaml` |

ファイルが存在しない場合、自動読み込みは省略します。環境変数だけでの運用もできます。

## YAMLの書式

`config init` が作成するテンプレートは [config.example.yaml](../internal/config/config.example.yaml) です。接続先などを入力し、認証情報は `credentials set` で登録します。

```yaml
credentials:
  store: keyring

argoWorkflow:
  server: https://argo.example.com
  namespace: argo-workflows

s3:
  endpoint: https://s3.example.com
  region: us-east-1
  bucket: simworkflows
  forcePathStyle: true

registry:
  url: https://ghcr.io

simScope:
  url: ""

httpTimeout: 30s
```

`argoWorkflow`・`s3`・`registry` は既存の `ARGO_WORKFLOW_*`・`S3_*`・`REGISTRY_*` と同じ設定です。`simScope.url` は `SIMSCOPE_URL`、`httpTimeout` は `HTTP_TIMEOUT` に対応します。`simScope.url` は任意設定で、利用するSimScopeのURLを入力してください。未設定の場合は `w` によるブラウザ表示時に設定方法を案内します。全項目の対応と必須条件は、このページの設定項目一覧を参照してください。
テンプレートには名前空間として `argo-workflows` を記載しています。接続先の名前空間へ変更してください。

未知の項目・重複キー・型の不一致・複数のYAML文書はエラーになります。エラーに認証情報の値は含めません。設定ファイルは1MiBまでです。

## 設定項目一覧

以下の必須条件はサービスを使うコマンドに適用します。ただし `experiment list`・`stop` はArgoの設定とHTTPタイムアウトだけを使用し、S3・レジストリの設定や認証情報は不要です。認証情報の項目はOSストアまたは環境変数から取得できればよく、YAMLへ書く必要はありません。`--version`、`config init`・`path`、オフラインの実験検証・計画・モジュール生成では接続設定は不要です。YAMLでは前項の対応する項目へ指定します。表のデフォルト値は、設定を省略した場合の値です。

| YAMLの項目 | 環境変数 | 必須 | デフォルト・用途 |
| --- | --- | --- | --- |
| `credentials.store` | `CREDENTIALS_STORE` | 任意 | 新規テンプレートは `keyring`。省略時は互換性のため `environment` |
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

YAMLの認証情報項目は互換モード用です。`keyring` 方式では無視されるため、ファイルへ記入せず `credentials set` で登録してください。

URLは `http://` または `https://` が必須です。URL内のユーザー情報、クエリ、フラグメントは受け付けません。
レジストリの認証は匿名・ユーザー名とパスワード・トークンのいずれかを選択できます。
`ARGO_WORKFLOW_NAMESPACE` と `S3_REGION` は省略するとデフォルト値を使い、明示的な空文字はエラーになります。

## 読み込み方式と優先順位

接続先などの通常設定は **環境変数 > `.env` > `config.yaml` > デフォルト値** の順です。値ごとに重ね合わせ、環境変数を明示的に空にした場合も優先します。YAMLの `""`・`false` も明示された値として扱います。

認証情報は `credentials.store` で扱いを選びます。

| 方式 | 認証情報の取得元 |
| --- | --- |
| `keyring` | サービス単位で環境変数を優先し、指定がなければOSストア。`.env`・YAMLの認証情報は使用しない |
| `environment` | 従来どおり環境変数 > `.env` > YAML。OSストアへはアクセスしない |

新規テンプレートは `keyring` です。既存の設定を壊さないため、項目自体を省略した場合は `environment` として動作します。ファイルに書いた古い認証情報をOSストアへ移す方法は [認証情報の管理](credentials.md) を参照してください。

キーストア方式で環境変数を上書きする場合は、そのサービスの認証情報を一組まとめて指定します。たとえばS3のキーIDだけを環境変数で指定しても、OSストアのシークレットキーとは組み合わせません。

## 別ファイルの指定

```sh
# 共通設定とは別のYAMLを使う
simworkflow --config /path/to/config.yaml experiment submit -f experiment.yaml

# 別の場所へテンプレートを作る
simworkflow --config ./config.local.yaml config init

# .envを読まず、環境変数と共通YAMLを使う
simworkflow --env-file='' experiment status WORKFLOW_NAME

# 設定ファイルを一切読まず、環境変数だけで使う
CREDENTIALS_STORE=environment simworkflow --config='' --env-file='' config check
```

`--config FILE` を明示した場合、そのファイルが存在しなければエラーです。共通YAMLは同時に読み込みません。`--config=''` はYAML読み込みだけを無効にします。これらの全体オプションは `experiment`・`map`・`config` より前に置きます。

`.env` はカレントディレクトリから自動で読みます。`--env-file FILE` で別ファイルを選択でき、`--env-file=''` でdotenv読み込みだけを無効にできます。設定値はシェル式や環境変数として展開しません。

互換モードで認証情報を入力した設定ファイルはGitへ追加しないでください。設定値はバイナリや実験の結果バンドルに埋め込みません。オフラインの `experiment validate`・`plan`・`modules` は接続設定を読みません。

## 開発用の.env

共通YAMLの代わりに、接続先をカレントディレクトリの `.env` に指定することもできます。新しいサンプルは `CREDENTIALS_STORE=keyring` を選び、認証情報の欄を含みません。

```sh
cp .env.example .env
chmod 600 .env
# 接続先・名前空間・バケット名を編集
simworkflow credentials set s3
simworkflow config check
```

`.env` は `KEY=value`、`export KEY=value`、単一引用符・二重引用符、コメントに対応します。値はリテラルで、変数展開・コマンド実行・エスケープ変換・複数行の値には対応しません。引用符を含む値はもう一方の引用符で囲んでください。`.env` と `.env.*` はGit管理から除外しています（`.env.example` を除く）。

既存の `.env` に認証情報を保存する従来方式は、`CREDENTIALS_STORE=environment` で利用できます。これは平文ファイルの読み込みを許可する互換モードです。[OSストアへの移行手順](credentials.md#既存のenvyamlから移行する) を参照してください。

## 接続確認

`config check` は設定の不足・形式を検証します。キーストア方式ではOSストアを参照しますが、接続先サービスへのリクエストは送りません。`config check --connect` はArgo、S3、レジストリへの読み取りアクセスを確認します。どちらも認証情報の値を表示しません。確認する権限やエラーの対処は [サービスへの接続確認](connections.md) を参照してください。

[ドキュメント一覧へ](README.md)
