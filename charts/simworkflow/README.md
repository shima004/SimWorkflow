# SimWorkflow Helm Chart

SimWorkflowの実行基盤を導入します。CLIは手元のPCで使い、クラスタにはCLI常駐サーバーを作りません。

| コンポーネント | 既定の構成 |
| --- | --- |
| Argo Workflows | 公式Chart 2.0.16 / Argo v4.1.6、対象はリリースの名前空間 |
| PostgreSQL | 17.6、ノード状態のオフロードとWorkflowアーカイブを有効化 |
| S3 | RustFS 1.0.4、単一Pod・100Gi PVC、実験用バケットを作成 |
| レジストリ | Distribution 3.1.2、Basic認証・50Gi PVC |
| 実験の権限 | `simworkflow` ServiceAccountにexecutor権限 |
| CLIの権限 | `simworkflow-client` ServiceAccountに送信・参照・停止の権限 |

SimScopeは任意の外部サービスとしてURLだけを設定します。シミュレーター・エージェントのイメージとマップは利用者が用意します。

## 前提

- Kubernetes 1.28以降、Helm 3.19以降。実験ノードは `linux/amd64`。
- PVCを割り当てられるStorageClass、または事前作成済みのPVC。
- Argo CRDの導入権限。CRDは公式依存ChartのインストールJobで管理します。
- 外部公開する場合は、Ingress ControllerとTLS証明書、名前解決と到達経路。

1名前空間につき1つのArgo Controllerを使用します。既存Argoが管理する名前空間へ、新しいControllerを重ねてインストールしないでください。既存環境を使う構成は後述します。

## 認証情報を用意する

同じ名前空間に次のSecretを事前作成します。名前は `values.yaml` の `existingSecret` で変更できます。

| Secret | 必須キー | 用途 |
| --- | --- | --- |
| `simworkflow-s3-credentials` | `access-key`, `secret-key` | RustFSとバケット作成Job |
| `simworkflow-postgresql-credentials` | `username`, `password` | PostgreSQLとArgo |
| `simworkflow-registry-auth` | `htpasswd`, `http-secret` | bcrypt形式の認証ファイルとRegistry内部の署名キー |

パスワードマネージャーやExternal Secretsなどから供給できます。以下はBash・OpenSSL・`htpasswd`を使い、認証情報をファイルやコマンド履歴に書かずにSecretを作る例です。既存Secretを上書きしません。`htpasswd` は `apache2-utils` 等で提供されます。

```bash
kubectl create namespace simworkflow

{
  printf 'access-key=%s\n' "$(openssl rand -hex 12)"
  printf 'secret-key=%s\n' "$(openssl rand -hex 32)"
} | kubectl -n simworkflow create secret generic simworkflow-s3-credentials \
    --from-env-file=/dev/stdin

{
  printf 'username=argo\n'
  printf 'password=%s\n' "$(openssl rand -hex 32)"
} | kubectl -n simworkflow create secret generic simworkflow-postgresql-credentials \
    --from-env-file=/dev/stdin

# Registryのパスワードを対話入力。後でCLIとimagePullSecretにも同じ値を使います。
{
  printf 'htpasswd=%s\n' "$(htpasswd -nB simworkflow)"
  printf 'http-secret=%s\n' "$(openssl rand -hex 32)"
} | kubectl -n simworkflow create secret generic simworkflow-registry-auth \
    --from-env-file=/dev/stdin
```

Kubernetes Secret自体の管理・暗号化・アクセス制限はクラスタ側で行います。ChartはSecretの値を参照するだけで、Helmのvaluesや出力へ複製しません。

## 一式をインストールする

リポジトリのルートで実行します。

```sh
helm dependency build charts/simworkflow
helm lint charts/simworkflow --strict

helm upgrade --install simworkflow ./charts/simworkflow \
  --namespace simworkflow \
  -f charts/simworkflow/examples/ingress.yaml \
  --wait --wait-for-jobs --timeout 15m
```

先に [examples/ingress.yaml](examples/ingress.yaml) のホスト名、Ingressクラス、TLS Secret名を自分の環境へ変更してください。この例はingress-nginx向けです。他のIngress Controllerでは、大きなアップロードの許可・転送時間・バッファ設定も合わせて変更します。証明書はChartでは発行しません。

既定のServiceはClusterIPです。Ingressの代わりに `s3.service.type`、`registry.service.type`、`argo-workflows.server.serviceType` へ `LoadBalancer` / `NodePort` を設定することもできます。HTTPで使うレジストリはDocker・containerd側の設定も必要です。Chartはノード設定を変更しません。

**S3の `s3.externalURL` はCLIと実験Podの両方から、`registry.externalURL` はCLI・Dockerのビルド環境・全実験ノードから到達できるURLにします。** `localhost` のport-forwardはS3の署名付きURLやノードのimage pullには使えません。IngressはS3のHostとパスを書き換えずに転送し、TLS証明書をCLI・Pod・ノードで信頼できるようにします。

URLを空にすると生成したCLI設定は `.svc` の内部URLを使います。これはクラスタ内でCLIを使う場合向けです。外部のCLIでは公開URLを指定します。`s3.externalURL` は外部S3を使う指定にも使いますが、それだけではRustFSは無効になりません。無効化は `s3.enabled: false` です。

## CLIを接続する

```sh
# 接続先だけを出力します。認証情報は含みません。
kubectl -n simworkflow get configmap simworkflow-cli-config \
  -o jsonpath='{.data.config\.yaml}' > config.yaml

simworkflow --config ./config.yaml credentials set s3
simworkflow --config ./config.yaml credentials set argo
simworkflow --config ./config.yaml credentials set registry
simworkflow --config ./config.yaml config check --connect
```

S3には用意したSecretの `access-key` / `secret-key`、Registryにはhtpasswd作成時のユーザー名とパスワードを、CLIの非表示プロンプトで登録します。自動生成したS3キーを取り出すには、管理者権限で以下を実行し、値を資格情報ストアへ登録してください。公開ログや共有端末へ出力しないでください。

```sh
kubectl -n simworkflow get secret simworkflow-s3-credentials -o jsonpath='{.data.access-key}' | base64 --decode
kubectl -n simworkflow get secret simworkflow-s3-credentials -o jsonpath='{.data.secret-key}' | base64 --decode
```

Argoは既定で `client` 認証です。CLI専用ServiceAccountの短期トークンを発行し、`credentials set argo` に入力します。トークンの有効期間はAPI Serverの設定にも依存します。期限後は再発行します。

```sh
kubectl -n simworkflow create token simworkflow-client --duration=1h
```

認証付きレジストリから実験ノードがpullするには、同じ資格情報の `kubernetes.io/dockerconfigjson` Secretも必要です。名前空間に用意し、次のように指定します。

```yaml
workflow:
  imagePullSecrets:
    - name: simworkflow-registry-pull
```

`workflow.serviceAccount.create: false` の場合、既存ServiceAccountの `imagePullSecrets` は管理者が設定します。Argo Controller/ServerのSecretやPodへRegistryのパスワードを渡す必要はありません。

既定の実験用ServiceAccountは `simworkflow` なので通常の `experiment submit` が使えます。名前を変更した場合は `--service-account NAME` を渡してください。生成したCLI接続設定を共通設定の保存先へ配置すれば、以後 `--config` は不要です。[接続設定](../../docs/configuration.md) を参照してください。

## 既存サービスへ切り替える

[examples/existing-services.yaml](examples/existing-services.yaml) では全サービスを無効化し、SimWorkflowのRBACと認証情報を含まないCLI ConfigMapだけを導入します。Argoの実行名前空間へインストールしてください。既存リソースをHelmで取り込む処理はありません。

```sh
helm upgrade --install simworkflow ./charts/simworkflow \
  --namespace argo-workflows \
  -f charts/simworkflow/examples/existing-services.yaml
```

既存ServiceAccountを使う場合は、作成を無効化し、使用する名前を指定します。ChartのRoleBindingはそのアカウントへ権限を付与します。

```yaml
workflow:
  serviceAccount: { create: false, name: existing-worker }
  clientServiceAccount: { create: false, name: existing-client }
```

既存のRole・RoleBindingもそのまま使う場合は `workflow.rbac.create: false` を指定します。特に手動のRBACサンプルを適用済みの環境では、既存リソースのHelm所有権エラーを避けるため、この設定とServiceAccountの作成無効化を使用してください。この場合は、送信・停止・executorに必要な権限が既にあることを管理者が確認します。

各サービスは独立に切り替えられます。既存S3の場合、`s3.enabled: false` と `s3.externalURL` を指定し、バケットを用意済みなら `s3.bucketInit.enabled: false` にします。作成Jobを使う場合だけ、S3用Secretをクラスタへ用意します。リージョン・パス形式は既存サービスへ合わせます。

PostgreSQLだけ外部にする例は [examples/external-postgresql.yaml](examples/external-postgresql.yaml) です。Argoと同じ名前空間にDB接続Secretが必要です。外部DBには初回マイグレーションでテーブルを作成できる権限を付与し、Argo Controllerの初期化が完了してから利用してください。ChartはSQLを先に手作業で作ったり、`skipMigration` を設定したりしません。

## リソース・永続データ

PostgreSQLは `postgresql.resources`、RustFSは `s3.resources`、Registryは `registry.resources`、Argoは `argo-workflows.controller.resources` / `argo-workflows.server.resources` でCPU・メモリを変更します。実験Podのリソースと並列数は従来どおり実験YAMLへ指定します。大規模実験で必要なArgoの設定は `argo.controllerConfig` へ追加します。`persistence` は `argo.persistence` / `postgresql` から生成するため、この追加設定では上書きできません。

```yaml
postgresql:
  resources:
    requests: { cpu: "1", memory: 2Gi }
    limits: { cpu: "2", memory: 4Gi }
  persistence: { storageClass: fast-ssd, size: 50Gi }
s3:
  persistence: { storageClass: bulk-storage, size: 1Ti }
argo-workflows:
  controller:
    resources:
      requests: { cpu: "1", memory: 2Gi }
      limits: { cpu: "4", memory: 8Gi }
```

各 `persistence.storageClass` は空でクラスタ既定、`"-"` でStorageClassなしです。既存PVCは `persistence.existingClaim` で指定し、Chartでは作成しません。空でなければ、PVCのサイズ・StorageClassは既存側で管理します。ボリュームは各コンテナのfsGroup（PostgreSQL:999、RustFS:10001、Registry:10000）で書き込める必要があります。NFSのroot squash等はストレージ側で調整してください。

同梱のDB・S3・Registryは各1Podで、HA構成やバックアップ機構は含みません。データ容量・バックアップ・復旧手順は運用環境に合わせて用意し、必要なら外部のHAサービスへ切り替えます。イメージやDBのメジャーバージョンを変更する際のデータ移行は自動実行しません。

Argoは状態のオフロードとアーカイブを有効にします。Podは成功・失敗とも完了後10分で削除します。Workflow自体のTTLは設定しません。S3への収集が完了したログ・スコアはPod削除後も取得できます。Argo自身のPodログのS3アーカイブは設定せず、実験YAMLの `outputs.collect` で指定するSimWorkflowの保存を利用します。

`helm uninstall` でも既定のPVCは `helm.sh/resource-policy: keep` により残します。Secretも外部管理なので削除しません。PVC・バックアップを破棄する場合は別途明示的に操作してください。DBやS3のSecretの変更は、既存DBユーザーのパスワード変更やコンテナの再起動まで自動では行いません。

## 検証・パッケージ化

```sh
helm dependency build charts/simworkflow
helm lint charts/simworkflow --strict
go test -v ./charts/simworkflow/tests
helm template simworkflow ./charts/simworkflow --namespace simworkflow
helm package charts/simworkflow --destination dist
```

Chartの生成テストは既存サービス・外部DB・Ingress・PVC再利用・権限・CLI設定の整合性を確認します。依存Chartを取得していない通常のGoテストではこの検査をスキップし、専用のGitHub Actionsで依存を取得して必ず実行します。共有クラスタへのインストールを伴う検査ではありません。

参照：[Argo公式Chart](https://github.com/argoproj/argo-helm/tree/main/charts/argo-workflows)、[Argo永続化](https://argo-workflows.readthedocs.io/en/release-4.1/workflow-archive/)、[RustFS](https://docs.rustfs.com/en/installation/container/docker)、[Distribution設定](https://distribution.github.io/distribution/about/configuration/)。
