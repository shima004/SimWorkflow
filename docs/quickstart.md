# 最初の実験

登録したマップで、1構成・1シードの実験を5ステップ実行し、スコアを含む要約を取得します。CLIを [インストール](installation.md) した後、作業用ディレクトリで進めてください。

この手順ではJavaエージェントを例にします。先に [Javaテンプレート](../templates/agents/java/README.md) と [ビルド・登録](agent-build.md) に従い、実行ノードから取得できるイメージを用意します。Pythonを使う場合は [Pythonテンプレート](../templates/agents/python/README.md) を参照してください。

## 1. 接続と実行環境を確認する

[インストール](installation.md) と [接続設定](configuration.md) を済ませ、次を実行します。

```sh
simworkflow config check --connect
```

3サービスの確認がすべて成功することを確認します。この確認は読み取りアクセスの検査です。実験の送信・結果の書き込み・実行ノードからのイメージ取得までを保証するものではありません。

Argo Workflows、結果保存用のS3バケット、エージェントイメージ、実行Pod用のServiceAccountが必要です。実行用ServiceAccountの既定名は `simworkflow` です。管理者から別の名前を指定された場合は、送信時に `--service-account NAME` を付けてください。まだ用意していない場合は [実行環境の準備](distribution.md) を参照してください。SimScopeはこの手順には不要です。

## 2. マップを登録する

手元にあるRCRSマップの `map/` と `config/` を、次の構成で `my-map/` の下に置きます。`map.gml`・`scenario.xml` と、`!include` などで参照する設定ファイルを含めてください。このCLIはマップ自体を生成しません。

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

登録済みなら再登録は不要です。同じ名前・バージョンは更新できません。形式やバージョン管理の詳細は [マップ](maps.md) を参照してください。

## 3. 実験YAMLを用意する

以下を `experiment.yaml` として保存します。`spec.agents[0].image` を、用意したJavaエージェントのイメージ参照へ置き換えてください。`agent build --push` が表示したdigest形式の `image:` も使用できます。

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
        versions: [v1]
    seeds: [1]
  execution:
    maxParallelRuns: 1
    timeout: 30m
  outputs:
    collect: [score, simulatorLogs, agentLogs]
```

Pythonを使う場合は [Pythonテンプレート](../templates/agents/python/README.md) に従ってイメージを作り、`runtime: python` と対応するイメージを指定します。モジュールを省略した場合はイメージ内の元設定を使います。

この例は動作確認のために5ステップで終了します。比較実験では必要なステップ数へ変更してください。`agents[].resources` は消防・警察・救急の各コンテナに適用されます。この例ではシミュレーター2 CPU・8Gi、エージェント合計6 CPU・12Gi、結果収集1 CPU・256Miで、合計9 CPU・20Gi＋256Miを要求します。クラスタの空き容量に合わせて [種類別リソース](experiment-yaml.md#種類ごとの上書き) などを調整してください。

## 4. 検証して送信する

```sh
simworkflow experiment validate -f experiment.yaml
simworkflow experiment plan -f experiment.yaml
```

`validate` と `plan` はオフラインです。イメージの存在や登録マップへのアクセスまでは確認しません。`plan` の `Total runs` が `1` であることを確認してから送信します。

```sh
simworkflow experiment submit -f experiment.yaml
```

`submit` が登録マップのバージョンとハッシュを確定し、Argoへ送信します。既定のhelperとS3へのワーカー設定保存を使うため、通常は追加オプションは不要です。

送信に成功するとWorkflow名が表示されます。その名前を控え、以後の `WORKFLOW_NAME` に指定します。準備中にエラーになった場合は [トラブル対応](connections.md) を参照してください。

## 5. 進捗と結果を確認する

```sh
simworkflow experiment list
simworkflow experiment status --watch WORKFLOW_NAME
simworkflow experiment results WORKFLOW_NAME
simworkflow experiment download --include summary --stage simulation WORKFLOW_NAME
```

実行は事前計算（`precompute`）→ 本実行（`simulation`）の順に進みます。Workflow全体が `Succeeded` になるまで監視してください。TUIは `q` で閉じられ、閉じても実験は継続します。端末でTUIを使えない場合は `status --watch --plain WORKFLOW_NAME` を使います。

`results` の `stage: simulation`・`phase: Succeeded` が本実行の成功を示します。`score` が取得したスコアです。ダウンロード先は既定で `artifacts/WORKFLOW_NAME/` となり、`<run-id>/simulation/summary.json` に同じ要約を保存します。`runs.csv` と `runs.json` で実行条件とファイルの対応を確認できます。5ステップでのスコアは動作確認用です。

失敗した場合は、[進捗の見方](progress.md#進捗の見方) と [トラブル対応](connections.md) で状態を確認します。原因を解消した後、[失敗分の再送信](retry.md) を利用できます。

途中で実験を停止する場合は `simworkflow experiment stop WORKFLOW_NAME` を使います。[停止時の結果保存と必要な権限](progress.md#実験を停止する) を確認してください。

モジュールの候補を増やす場合は [実験YAML](experiment-yaml.md)、詳しい操作は [進捗・スコア](progress.md) と [ダウンロード](download.md) を参照してください。

[ドキュメント一覧へ](README.md)
