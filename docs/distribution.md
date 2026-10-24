# 実行環境の準備と配布

クラスタを準備する管理者と、CLI・helperを公開する開発者向けのガイドです。既存の実行環境を利用する場合は [インストール](installation.md) → [接続設定](configuration.md) → [最初の実験](quickstart.md) へ進んでください。

## 必要なサービス

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

S3バケットを作成し、[マップ](maps.md) を登録します。同じマップ版への並行登録にはS3の条件付きPUTが正しく動作することが必要です。検証環境で確認した制約はマップのガイドに記載しています。

`examples/validation/` にある内部レジストリの参照や実験名は検証時の記録です。別環境へそのまま送信せず、利用できるイメージ・マップへ置き換えてください。

## 公開helperを利用する

helperはシミュレーター・エージェントの起動管理、S3からの設定取得、事前計算データの受け渡し、進捗・結果の保存を担当するイメージです。利用者がローカルにビルドする必要はありません。

CLIは次の検証済み公開イメージをデフォルトにしています。

```text
ghcr.io/shima004/simworkflow-worker@sha256:2e204474aa5c348c0d2196cc83adde038170fd9885bb09c2e83b4a4fd31cc027
```

通常は追加オプションなしで送信できます。ワーカー設定の保存先はS3がデフォルトです。

```sh
simworkflow experiment submit -f experiment.yaml
```

別のhelperを利用するときだけ `--helper-image IMAGE` を指定します。`latest` は更新されるため、再現性が必要な実験ではdigestやコミット固定タグを使います。新しいhelperの公開で、CLIのデフォルトdigestや送信済み実験の参照先が自動更新されることはありません。

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

## CLIをリリースする

[release.yml](../.github/workflows/release.yml) は `v*` タグのpushでテスト・vet・依存検査を行い、Linux／macOS／Windowsのamd64・arm64向けバイナリを作成します。Linux／macOSはtar.gz、Windowsはzipで配布し、各アーカイブには実行ファイルと利用手順を含めます。LICENSEが存在する場合は同梱します。SHA256の `checksums.txt` も添付します。

バイナリの `simworkflow --version` はタグとソースコミットを表示します。ビルドではCGOを無効にし、`.env` などの利用環境の設定は取り込みません。macOS・Windows向けバイナリの署名は行いません。

新しいバージョンのタグを、リリースするコミットに作成してpushします。以下は `v0.1.0` の例です。既存のタグは再作成せず、pushだけを行います。

```sh
git push origin main
git tag -a v0.1.0 -m "SimWorkflow v0.1.0"
git push origin v0.1.0
```

`v0.*` と `-rc.1` などの接尾辞があるタグはプレリリース、それ以外は正式リリースになります。全ファイルをアップロードしてから公開し、途中で失敗した場合はドラフトが残ります。同じタグのActionsを再実行するとドラフトへのアップロードを再試行できます。公開済みReleaseは上書きせず、新しいタグを作成してください。既存のhelper公開ワークフローも同じタグで実行されます。CLIが使用するデフォルトhelperは検証済みdigestのままです。

ローカルで配布物を作る手順は [開発ガイド](development.md) を参照してください。

## helperを継続公開する

[helper-image.yml](../.github/workflows/helper-image.yml) は次の処理を行います。

- Pull request：Goのraceテスト・vet・依存整合性検査
- main更新：上記検査後、GHCRへ `latest` と `sha-<コミットSHA>` を公開
- `v*` タグ：上記検査後、同名タグと `sha-<コミットSHA>` を公開
- 手動実行：指定したrefのコミット固定タグを公開（mainならlatestも更新）

公開にはActionsの `GITHUB_TOKEN` とジョブ単位の `packages: write` を使用します。個人用のS3キーやArgoトークンをActionsへ設定する必要はありません。Dockerfileは `cmd/` と `internal/` だけをコピーし、公開イメージへ `.env`・マップ・取得済みログを取り込みません。

GHCRでは新しいパッケージの初期公開範囲はprivateです。初回登録後、パッケージ設定の **Change visibility → Public** を設定し、認証なしでpullできることを確認してください。リポジトリをPublicにする操作とは別です。既存の実験定義のイメージ参照は自動変更しません。

参考：[GitHub公式のGHCR公開手順](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Container registryの公開範囲](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

公開済みイメージの初回確認結果は [検証記録](validation.md#公開helperの初回確認) を参照してください。

[ドキュメント一覧へ](README.md)
