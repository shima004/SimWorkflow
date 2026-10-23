# 実験定義YAML v1alpha1

実験条件を記述するための仕様です。CLIがこの定義から実行ごとの条件を展開し、Argo Workflowへ変換します。
YAMLの検証、計画の展開、事前計算付きの実行、結果要約の表示を提供します。[実行手順](execution.md) を参照してください。

完全な定義例は [module-comparison.yaml](../examples/experiments/module-comparison.yaml) を参照してください。
イメージ、マップの場所、モジュール名は例示用です。
Java版サンプルはADFの実際のキーを使用し、独自モジュールの候補は仮名としています。
Python版は [python-module-comparison.yaml](../examples/experiments/python-module-comparison.yaml) を参照してください。
シミュレーターには `ghcr.io/shima004/rcrs-server:v1.0.4` を使用します。[起動仕様の確認結果](simulator.md)も参照してください。

## 基本構造

| 項目                             | 内容                                                     |
| -------------------------------- | -------------------------------------------------------- |
| `apiVersion`                     | `simworkflow/v1alpha1`                                   |
| `kind`                           | `Experiment`                                             |
| `metadata.name`                  | 実験名                                                   |
| `metadata.description`           | 任意の説明                                               |
| `spec.simulator.image`           | シミュレータのコンテナイメージ                           |
| `spec.agents`                    | 比較対象となるエージェント定義の配列                     |
| `spec.conditions.maps`           | マップ名 `name` と配置先 `source` の配列                 |
| `spec.conditions.seeds`          | 乱数シードの整数配列                                     |
| `spec.execution.maxParallelRuns` | 実験全体で同時実行するシミュレーション数の上限。正の整数 |
| `spec.execution.timeout`         | 1回の実行の制限時間。`30m` などの正の期間                |
| `spec.outputs.collect`           | 収集対象。`score`、`simulatorLogs`、`agentLogs`          |

エージェント定義、マップ、シードを組み合わせて実行します。
`agents` は比較対象の一覧です。配列の全エージェントを1回のシミュレーションに同時投入する意味ではありません。
エージェント名とマップ名は、それぞれ実験内で一意とします。

`spec.simulator.resources` と各 `spec.agents[].resources` に、割り当てるCPU・メモリを指定します。エージェントは種類ごとのコンテナに適用します。詳細は次節を参照してください。

サービスの接続先・認証情報はCLIの環境変数で設定し、このYAMLには含めません。
マップの `s3://bucket/key` はS3オブジェクトの参照先です。結果の保存先バケットを指定する `S3_BUCKET` とは別に、参照するバケットを表します。

## CPU・メモリ

シミュレータとエージェントのそれぞれに `resources.cpu` と `resources.memory` を指定します。

```yaml
spec:
  simulator:
    image: registry.example.com/rescue-simulator:v1
    resources:
      cpu: 2
      memory: 4Gi

  agents:
    - name: comparison
      image: registry.example.com/rescue-agent:v1
      resources:
        cpu: 4
        memory: 8Gi
```

| 項目 | 指定方法 | 例 |
| --- | --- | --- |
| `resources.cpu` | コア相当のCPU量を表す正の数値。小数も可。最小値・刻みは `0.001` コア | `2`、`0.5` |
| `resources.memory` | 正の整数と単位 `Mi` または `Gi` を組み合わせた文字列 | `512Mi`、`4Gi` |

初期仕様ではシミュレータと各エージェントの両方でCPU・メモリを必須とします。
省略、ゼロ、負の値、対応しない単位はエラーとし、暗黙のデフォルト値は設けません。

Argo Workflowへの変換では、それぞれのコンテナの `resources.requests` と `resources.limits` の両方に同じ値を設定します。
CPU量の指定は専用の物理コアを占有する指定ではありません。単位と制限の動作は [Kubernetesのリソース管理](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/) に従います。

エージェントのリソース指定は、その定義から展開されるすべてのモジュール構成に共通で適用します。
各種類のコンテナ内に複数のエージェントプロセスがある場合、指定値はその種類全体の合計です。
リソース量は組み合わせの軸には含めず、実行回数を変えません。

例で種類別の上書きを省略すると、1回の実行にシミュレーター2 CPU・4Gi、各種類のエージェント4 CPU・8Giを割り当てます。
3種類の合計を含めて14 CPU・28Gi、4回同時実行では56 CPU・112Giです。結果収集などの補助コンテナに必要なリソースはこの計算に含みません。

これらの設定はコンテナに対するもので、JVMなどのアプリケーション内部のメモリ設定は別途決定します。

### 種類ごとの上書き

```yaml
resources:
  cpu: 2
  memory: 4Gi
resourcesByRole:
  fireBrigade:
    cpu: 2
    memory: 4Gi
  policeForce:
    cpu: 1
    memory: 2Gi
  ambulanceTeam:
    cpu: 4
    memory: 8Gi
```

`resourcesByRole` はエージェント定義内に指定します。各上書きはCPU・メモリの両方を必須とし、省略した種類は `resources` を使用します。
`resources` は引き続き必須です。3種類を別コンテナで起動し、各種類の全個体が指定値を共有します。

| キー | 同じコンテナで動作する対象 |
| --- | --- |
| `fireBrigade` | 消防隊と消防署 |
| `policeForce` | 警察隊と警察署 |
| `ambulanceTeam` | 救急隊と救急センター |

センターだけの個別指定や未知の種類名は受け付けません。事前計算と本実行では同じリソース設定を使います。
種類別リソースも構成ID・実行IDの生成対象になり、結果の実行条件として保存します。

## 固定モジュールと組み合わせ

各エージェントに `name` と `image` を指定します。
`runtime` に `java` または `python` を指定し、起動方法と事前計算データの配置を選びます。省略時は `java` です。
`modules` には固定するモジュールを、`matrix.modules` には切り替えるモジュールの候補を指定します。
モジュールのキーは差し替え箇所、値は実装の識別子です。初期仕様では実装の識別子を文字列で指定します。

```yaml
agents:
  - name: comparison
    image: registry.example.com/rescue-agent:v1
    resources:
      cpu: 4
      memory: 8Gi
    modules:
      SampleRoadDetector.Clustering: adf.impl.module.algorithm.KMeansClustering
    matrix:
      modules:
        SampleRoadDetector.PathPlanning:
          - adf.impl.module.algorithm.DijkstraPathPlanning
          - my_team.module.algorithm.CustomPathPlanning
```

この例では、固定したClusteringと、2種類のPathPlanningを組み合わせた2構成を生成します。
`matrix` を省略したエージェントは、固定設定の1構成として扱います。
`modules` と `matrix.modules` に同じキーを指定することはエラーとし、暗黙の上書きはしません。

`matrix.modules` の各候補配列の直積から全構成を生成します。たとえばpathPlanningが2候補、clusteringが2候補なら4構成です。
候補配列の空配列、重複する候補、空の実装識別子はエラーとします。`matrix` を指定する場合は、1つ以上のモジュール軸が必要です。

## 組み合わせの除外

`matrix.exclude` で、実行しない構成を指定します。

```yaml
exclude:
  - modules:
      SampleRoadDetector.PathPlanning: adf.impl.module.algorithm.DijkstraPathPlanning
      SampleRoadDetector.Clustering: my_team.module.algorithm.CustomClustering
```

1つの除外条件に記述された項目がすべて一致した構成を除外します。
複数の除外条件がある場合は、いずれかに一致した構成を除外します。
一部のモジュールだけを指定した場合、その他のモジュールの値にかかわらず一致した構成を除外します。
比較は実装識別子の完全一致です。

除外条件には `matrix.modules` 内のキーと、その軸に存在する候補を指定します。
空の除外条件、存在しないキー・候補はエラーとします。
除外の結果、エージェントの構成が0件になった場合もエラーとします。

## 実行回数と結果

総実行回数は、次の式で求めます。

```text
全エージェントの除外後の構成数の合計 × マップ数 × シード数
```

サンプルはモジュール2候補 × 2候補から1構成を除外するため、3構成です。
マップ1件 × シード3件を掛け、合計9回のシミュレーションを実行する定義になります。
除外を省略すれば12回です。`maxParallelRuns` は総実行回数を変えません。

マップとシードはそれぞれ1件以上必要とし、重複するマップ名・シードはエラーとします。
反復回数はまだ導入せず、まずは指定した各シードで1回ずつ実行する仕様とします。

各実行の結果には、実験を識別するID、エージェント名とイメージ、展開後のモジュール構成、シミュレータとエージェントそれぞれのCPU・メモリ設定、マップ、シード、実行状態を記録します。
同じ実験定義を再実行した場合も、実験の実行を識別できるようにします。
`simworkflow experiment plan -f FILE` で、展開後の構成と総実行回数を確認できます。
`--format json` でリソースやマップの参照先を含む実行計画を出力します。
既定では10,000回まで展開し、`--max-runs` で上限を明示的に変更できます。

## 検証と計画の識別子

未知の項目、重複したYAMLキー、複数のYAMLドキュメントはエラーとします。
実験名・エージェント名・マップ名は、小文字英数字とハイフンによる1–63文字とし、先頭と末尾は英数字にします。
モジュールキーと実装識別子には、ドット区切りの識別子を指定します。
CPUは引用符のない数値で指定し、JSON計画では精度を維持するため文字列で出力します。
タイムアウトは正の秒数に相当する期間とし、秒未満の端数は受け付けません。

軸名の辞書順、候補の記述順、マップの記述順、シードの記述順に展開します。
各構成に `config-...`、各実行に `run-...` のIDを付け、解決後の設定から生成します。
同じ計画は同じIDになります。これらは実行計画上の識別子であり、再実行の履歴を区別する実行IDは送信・生成のたびに新しく発行します。

## コンテナとの接続と今後の拡張

ADFの設定キーは [モジュール切り替え](adf-module-settings.md)、元設定の配置と起動契約は [Java](../templates/agents/java/README.md)／[Python](../templates/agents/python/README.md) を参照してください。
実行時にはイメージ内の元設定へ選択したモジュールだけを反映します。候補の実装はイメージに含めておきます。
マップZIPの形式、事前計算の順序、スコア・ログの保存先は [実行手順](execution.md) に記載しています。

モジュール固有のパラメータ指定、任意の起動コマンド、実験間の事前計算キャッシュは今後の拡張です。
