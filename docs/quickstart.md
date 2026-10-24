# 最初の実験

登録したマップで、1構成・1シードの実験を実行します。コマンド例はCLIをインストール済みの状態を前提にします。

## 1. 接続と実行環境を確認する

[インストール](installation.md) と [接続設定](configuration.md) を済ませ、次を実行します。

```sh
simworkflow config check --connect
```

Argo Workflows、結果保存用のS3バケット、エージェントイメージ、実行Pod用のServiceAccountが必要です。まだ用意していない場合は [実行環境の準備](distribution.md) を参照してください。SimScopeはこの手順には不要です。

## 2. マップを登録する

`my-map/` の下に、利用するマップの `map/` と `config/` を置きます。

```text
my-map/
  map/
    map.gml
    scenario.xml
  config/
    kernel.cfg
    common.cfg
    その他、設定から参照するファイル
```

```sh
simworkflow map register --name my-map --version v1 --path ./my-map
simworkflow map inspect --name my-map --version v1
```

登録済みなら再登録は不要です。同じ名前・版は更新できません。形式や版管理の詳細は [マップ](maps.md) を参照してください。

## 3. 実験YAMLを用意する

以下を `experiment.yaml` として保存します。`agents[0].image` は [Javaテンプレート](../templates/agents/java/README.md) でビルドした、自分のエージェントイメージに置き換えてください。

```yaml
apiVersion: simworkflow/v1alpha1
kind: Experiment
metadata:
  name: first-experiment
spec:
  simulator:
    image: ghcr.io/shima004/rcrs-server:v1.0.4
    resources: {cpu: 2, memory: 8Gi}
    parameters: {timesteps: 5}
  agents:
    - name: baseline
      runtime: java
      image: registry.example.com/my-team/agent-java:v1
      resources: {cpu: 2, memory: 4Gi}
  conditions:
    maps:
      - name: my-map
        version: v1
    seeds: [1]
  execution:
    maxParallelRuns: 1
    timeout: 30m
  outputs:
    collect: [score, simulatorLogs, agentLogs]
```

Pythonを使う場合は [Pythonテンプレート](../templates/agents/python/README.md) に従ってイメージを作り、`runtime: python` と対応するイメージを指定します。モジュールを省略した場合はイメージ内の元設定を使います。

この例は接続・実行経路を確認するために5ステップで終了します。比較実験では必要なステップ数へ変更してください。`agents[].resources` は消防・警察・救急の各コンテナに適用されるため、この例ではエージェント全体で6 CPU・12Giを確保します。シミュレーターと収集コンテナの分も必要です。[種類別リソース](experiment-yaml.md#種類ごとの上書き) で個別に変更できます。

## 4. 検証して送信する

```sh
simworkflow experiment validate -f experiment.yaml
simworkflow experiment plan -f experiment.yaml
simworkflow experiment submit -f experiment.yaml
```

`validate` と `plan` はオフラインです。イメージの存在や登録マップへのアクセスまでは確認しません。`submit` が登録マップの版とハッシュを確定し、Argoへ送信します。公開helperとS3へのワーカー設定保存がデフォルトなので、通常は追加オプションは不要です。

送信時に表示されたWorkflow名を、以後の `WORKFLOW_NAME` に指定します。

## 5. 進捗と結果を確認する

```sh
simworkflow experiment list
simworkflow experiment status --watch WORKFLOW_NAME
simworkflow experiment results WORKFLOW_NAME
simworkflow experiment download --include summary --stage simulation WORKFLOW_NAME
```

TUIは `q` で閉じられます。閉じても実験は継続します。実行は事前計算 → 本実行の順に進み、要約のダウンロード先は `artifacts/WORKFLOW_NAME/` です。

途中で実験を停止する場合は `simworkflow experiment stop WORKFLOW_NAME` を使います。[停止時の結果保存と必要な権限](progress.md#実験を停止する) を確認してください。

モジュールの候補を増やす場合は [実験YAML](experiment-yaml.md)、詳しい操作は [進捗・スコア](progress.md) と [ダウンロード](download.md) を参照してください。

[ドキュメント一覧へ](README.md)
