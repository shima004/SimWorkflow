# 実行環境の準備（管理者向け）

クラスタ・ストレージ・レジストリを準備する管理者向けのガイドです。既存の実行環境を利用する場合は [インストール](installation.md) → [接続設定](configuration.md) → [最初の実験](quickstart.md) へ進んでください。

## 必要なサービス

新規導入には [SimWorkflow Helm Chart](../charts/simworkflow/README.md) を利用できます。Argo Workflows・PostgreSQL・RustFS・レジストリと必要な権限をまとめて導入し、各サービスを既存環境へ切り替えることもできます。認証情報は事前作成したKubernetes Secretを参照します。

| 対象 | 用意するもの |
| --- | --- |
| Kubernetes・Argo Workflows | ContainerSetを実行できる環境、対象名前空間、CLIと実行Podの権限 |
| S3互換サービス | マップ・ワーカー設定・進捗・結果を保存する非公開バケットとアクセスキー（OSストアまたは環境変数で管理） |
| イメージレジストリ | 実行ノードから取得できるシミュレーター・エージェント・helperのイメージ |
| SimScope（任意） | 利用する場合だけURLと接続経路を設定 |

CLIからArgo・S3・レジストリへ、実行PodからS3と各イメージへアクセスできる必要があります。CLIの接続設定だけで、Kubernetesノードのレジストリ認証やネットワーク設定は変更されません。

標準の実行Podは `linux/amd64` を対象にします。利用者向けCLIはLinux・macOS・Windowsのamd64／arm64向けです。

## 名前空間と実行権限

次のRBACサンプルは `argo-workflows` 名前空間と `simworkflow` ServiceAccountを使用します。管理者が利用環境へ合わせて変更・適用してください。CLIはRBACを自動変更しません。

- [client-rbac.yaml](../examples/kubernetes/client-rbac.yaml)：CLIからWorkflowを作成・参照・停止し、Podログを参照する権限
- [executor-rbac.yaml](../examples/kubernetes/executor-rbac.yaml)：実行Podのexecutor権限

実行用ServiceAccountは `experiment submit --service-account NAME` で選べます。デフォルトは `simworkflow` です。Argoの接続エラーは [接続確認](connections.md) を参照してください。

## シミュレーター・エージェント・マップ

シミュレーターは `ghcr.io/shima004/rcrs-server:v1.0.4` に対応します。エージェントは [Java](../templates/agents/java/README.md) または [Python](../templates/agents/python/README.md) のテンプレートで作成し、実行ノードから取得できるレジストリへ登録します。

S3バケットを作成し、[マップ](maps.md) を登録します。同じマップバージョンへの並行登録にはS3の条件付きPUTが正しく動作することが必要です。利用するS3互換サービスの動作条件は [マップのガイド](maps.md#保存と同時登録の条件) を確認してください。

## 公開helperを利用する

helperはシミュレーター・エージェントの起動管理、S3からの設定取得、事前計算データの受け渡し、進捗・結果の保存を担当するイメージです。利用者がローカルにビルドする必要はありません。

Release配布のCLIは、同じソースからビルド・公開・動作確認した `ghcr.io/shima004/simworkflow-worker` のdigestをデフォルトにします。Releaseに添付する `helper-image.txt` に対応するイメージ参照を記録します。

通常は追加オプションなしで送信できます。ワーカー設定の保存先はS3がデフォルトです。

設定の保存形式はCLIがhelperに合わせて自動選択します。通常は既定の `--config-storage auto` を使用してください。大規模な実験や旧helperとの互換性は [設定保存方式](execution.md#ワーカー設定とworkflowサイズ) を参照してください。

`io.simworkflow.result-zip=v1` を持つhelperでは結果をZIPで保存し、CLIからログを部分取得できます。旧helperはtar.gzを使用します。新しい保存形式を利用するには、CLIとhelperを両方更新してください。

```sh
simworkflow experiment submit -f experiment.yaml
```

別のhelperは `--helper-image IMAGE`、環境変数 `SIMWORKFLOW_HELPER_IMAGE`、共通設定の `workflow.helperImage` の順に優先して指定できます。詳しくは [helperの設定](configuration.md#helperイメージを選ぶ) を参照してください。`latest` は更新されるため、再現性が必要な実験ではdigestで固定します。公開済みCLIの既定値や送信済み実験の参照先は、自動更新しません。

## 独自helperをビルドする

ワーカーのコードを変更する場合に、このリポジトリのルートで実行します。

```sh
docker build --platform linux/amd64 -t registry.example.com/simworkflow/worker:v1 .
docker push registry.example.com/simworkflow/worker:v1
simworkflow experiment submit -f experiment.yaml \
  --helper-image registry.example.com/simworkflow/worker:v1
```

HTTPレジストリへの登録方法は利用するDocker環境に依存します。Dockerデーモンの設定を変更せずに登録する場合は、保存したイメージをcraneで転送する方法もあります。

```sh
go install github.com/google/go-containerregistry/cmd/crane@v0.22.1
docker save -o /tmp/simworkflow-worker.tar registry.example.com/simworkflow/worker:v1
crane push --insecure /tmp/simworkflow-worker.tar registry.example.com/simworkflow/worker:v1
```

S3から設定を取得できない旧ワーカーを使う場合だけ `--config-storage inline` を指定します。[Workflowサイズと設定保存方式](execution.md#ワーカー設定とworkflowサイズ) を確認してください。

## 大規模な実験のための設定

Workflowの定義が小さくても、実行中のノード状態は実行数に応じて増えます。大規模な実験では、ArgoのSQL接続を設定し、ノード状態をDBへ退避する `nodeStatusOffLoad: true` を有効にします。Argo Helm Chartでは既存の `controller.persistence` に追加します。

```yaml
controller:
  persistence:
    nodeStatusOffLoad: true
    # 既存のpostgresqlなどの接続設定は維持する
```

SimWorkflow Helm Chartを使う場合の値は [ChartのREADME](../charts/simworkflow/README.md) を参照してください。これは終了したWorkflowを保存する `archive: true` とは別機能です。定義そのものは小さくならないため、CLIの900KiBのサイズ検査も適用されます。

進捗確認には、退避したノード状態をArgo Server APIから取得できる必要があります。設定の詳細は [Argoの公式ガイド](https://argo-workflows.readthedocs.io/en/release-4.1/offloading-large-workflows/) を参照してください。

## 利用者へ渡す情報

導入後は、次の情報を利用者へ案内してください。

| 情報 | 利用者側の指定先 |
| --- | --- |
| CLIから到達できるArgo ServerのURL・対象名前空間 | `argoWorkflow.server`・`argoWorkflow.namespace` |
| S3のURL・リージョン・バケット名・パス形式の要否 | `s3` の各項目 |
| CLIから到達できるレジストリのURL | `registry.url` |
| 各サービスの認証方式と認証情報 | `credentials set` で登録。CIでは環境変数 |
| 実行用ServiceAccount名 | `submit --service-account NAME`。既定は `simworkflow` |
| 利用可能なイメージ・マップ名とバージョン | 実験YAML |
| 1 Podあたりに要求できるCPU・メモリと並列数の目安 | 実験YAMLの `resources`・`maxParallelRuns` |
| SimScopeのURLと接続可能な範囲（利用する場合） | `simScope.url` |

利用者の端末から `config check --connect` を実行し、続いて [最初の実験](quickstart.md) で送信・イメージ取得・S3への結果保存まで確認します。接続確認だけでは書き込み権限やノードからの到達性は確認できません。

CLI・helperの公開手順は [開発ガイド](development.md#cliをリリースする) にあります。

[ドキュメント一覧へ](README.md)
