# 事前計算付き実験の実行

1回の実験は、事前計算と本実行の2段階で構成します。
対象はこのリポジトリのテンプレートに従ったJava／Pythonエージェントと、`ghcr.io/shima004/rcrs-server:v1.0.4` です。

## 実行の順序

1. S3からマップZIPを取得し、実験専用ディレクトリに展開する。
2. 各コンポーネントの設定に同じシードを反映する。
3. 元のエージェント設定に、選択されたモジュール構成を反映する。
4. サーバーを `precompute` モードで起動し、接続準備完了を待つ。
5. エージェントを事前計算モードで接続する。
6. エージェントの正常終了と生成データを確認してから、事前計算用サーバーを停止する。
7. サーバーを `comprun --nogui` で新しく起動する。
8. 生成データを保持したまま、エージェントを事前計算フラグなしで起動する。
9. 本実行の全platoonエージェントが事前計算済みモードで接続したことをログで確認し、終了後に結果をS3へ保存する。

Argoでは1実験を1PodのContainerSetとして実行します。シミュレーターとエージェントのコンテナは、その中で各モードのプロセスを新しく起動します。
2段階の間で共有する `emptyDir` は実験ごとに独立しています。並列実験とのデータ混在や、他実験の事前計算データの再利用は行いません。
CPU・メモリはそれぞれのコンテナに指定値を適用し、2段階だからといって二重に確保しません。別途、結果保存用コンテナに0.1 CPU・256Miを割り当てます。

事前計算では、マップに存在する各platoon種別について1体ずつ接続します。本実行では `map/scenario.xml` に記載された各種別の数を接続します。
Javaは `PRECOMPUTED`、Pythonは構造化ログの `mode: 1`（PRECOMPUTED）を確認します。ログ形式はテンプレートが対象とするADFの版に依存します。
Javaの生成先は `/app/precomp_data` から実験専用領域へのリンク、Pythonの生成先は `launcher.yaml` で指定した実験専用領域です。

## マップZIPの形式

ZIPのルートに以下を配置します。

```text
map/
  map.gml
  scenario.xml
config/
  kernel.cfg
  common.cfg
  その他、includeで参照される設定ファイル
```

サーバーのマップ配布物全体をこの形式で保存してください。単一GMLファイルだけでは実行できません。
相対パスの外へ出るエントリ、シンボリックリンク、重複パス、2GiBを超えるアーカイブ・展開結果は受け付けません。
`random.seed` は展開した設定内で上書きします。元のS3オブジェクトには変更を加えません。

## イメージの準備

エージェントは [Java](../templates/agents/java/README.md) または [Python](../templates/agents/python/README.md) のテンプレートでビルドし、実行ノードからpullできるレジストリに登録します。
ワーカーはこのリポジトリのルートでビルドします。

```sh
docker build -t registry.example.com/simworkflow/worker:v1 .
docker push registry.example.com/simworkflow/worker:v1
```

HTTPレジストリへの登録ではDockerデーモンの設定に応じて追加の対応が必要です。
デーモン設定を変えずに登録する場合の例:

```sh
go install github.com/google/go-containerregistry/cmd/crane@v0.22.1
docker save -o /tmp/simworkflow-worker.tar registry.example.com/simworkflow/worker:v1
crane push --insecure /tmp/simworkflow-worker.tar registry.example.com/simworkflow/worker:v1
```

実行するKubernetesノード側も、そのレジストリへ接続できる必要があります。
現在の実行テンプレートは `linux/amd64` を対象にします。Java版は起動時にGradleをofflineモードで使用するため、依存ライブラリを含むテンプレートイメージを使ってください。

## Argoの実行権限

CLIの作成・参照権限は [client-rbac.yaml](../examples/kubernetes/client-rbac.yaml)、実行Podのexecutor権限は [executor-rbac.yaml](../examples/kubernetes/executor-rbac.yaml) を参照してください。
既存のRoleBindingで同等の権限があれば追加は不要です。CLIはこれらの設定を自動適用しません。

## 送信と結果の確認

実験YAMLの `runtime` に `java` または `python` を指定します。イメージ、マップのS3参照、モジュール候補は実際に用意したものを指定してください。

```sh
go run ./cmd/simworkflow -env-file .env experiment submit \
  -f experiment.yaml \
  --helper-image registry.example.com/simworkflow/worker:v1 \
  --service-account simworkflow

# submitが返したWorkflow名を指定。オプションは名前より前に置く
go run ./cmd/simworkflow -env-file .env experiment status --watch WORKFLOW_NAME
go run ./cmd/simworkflow -env-file .env experiment results WORKFLOW_NAME
```

`results` は実験ごとの要約をJSON配列で返します。実行状態、モジュール構成、シード、事前計算ファイル、スコア、S3の保存先を確認できます。
スコアは本実行の `kernel.log` に記録された最後の `Score:` または `Score=` の数値です。取得できない場合は `score` を省略し、値を推測しません。

S3では実行ごとに一意のプレフィックスを生成します。

```text
experiments/<execution-id>/plan.json
experiments/<execution-id>/<run-id>/summary.json
experiments/<execution-id>/<run-id>/bundle.tar.gz
```

バンドルには要約、事前計算データ、解決後のモジュール設定、シード反映後のサーバー設定、状態ファイルを保存します。
エージェントログ・サーバーログは `outputs.collect` の指定に従って追加します。
ワーカー開始後の失敗やタイムアウトでも結果保存を試みます。Pod自体の起動・イメージ取得・準備処理に失敗した場合や、強制削除・保存先への通信失敗では要約を保存できないため、Argoの状態を確認してください。

## 生成YAMLの確認

```sh
mkdir -p artifacts
go run ./cmd/simworkflow -env-file .env experiment render \
  -f experiment.yaml \
  --helper-image registry.example.com/simworkflow/worker:v1 \
  --output artifacts/workflow.yaml
```

`render` はS3オブジェクトの存在確認、期限付きURLの発行、実行計画のS3保存を行います。Argoには送信しません。
出力ファイルは0600で新規作成し、既存ファイルは上書きしません。
このファイルはそのままArgoへ送信でき、有効期限内は対象オブジェクトへのアクセス権を持ちます。Gitへの登録・共有は避けてください。`artifacts/` はGitとDockerの対象から除外しています。

PodにはS3の長期アクセスキーやArgoのトークンを渡しません。CLIが対象マップのGET、結果2ファイルのPUTだけを許可する署名付きURLを発行します。
署名URLの有効期限は24時間です。1実行20時間まで、計画上の総所要時間23時間までに制限します。キュー待ちで期限を超えた場合や元の一時認証情報が早く失効した場合は、再送信が必要です。
URLを含むワーカー設定は結果バンドルに含めません。

## タイムアウト

`execution.timeout` は事前計算と本実行を合わせたワーカーの処理時間です。
終了後の保存猶予として、PodとWorkflowには追加で180秒を設定します。
待機・準備に長時間かかった場合はArgo側の期限が先に到達することがあります。
`maxParallelRuns` は同時実行Pod数を制限します。
