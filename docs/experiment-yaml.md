# 実験定義YAML v1alpha1

比較するエージェント構成・マップ・シードと、実行時のリソースや収集対象を指定します。CLIが組み合わせを展開し、事前計算と本実行を行うArgo Workflowを作成します。最小限の完全な定義は [クイックスタート](quickstart.md#3-実験yamlを用意する) を参照してください。

このページのYAMLは、基本構造の表を除いて主に設定の抜粋です。各例に記載した階層へ追加してください。作成後は次のコマンドで形式と総実行回数を確認します。

```sh
simworkflow experiment validate -f experiment.yaml
simworkflow experiment plan -f experiment.yaml
```

完全な定義例は [module-comparison.yaml](../examples/experiments/module-comparison.yaml) を参照してください。
イメージ、マップの場所、モジュール名は例示用です。
JavaエージェントのサンプルはADFの実際のキーを使用し、独自モジュールの候補は仮名としています。
Pythonエージェントの定義例は [python-module-comparison.yaml](../examples/experiments/python-module-comparison.yaml) を参照してください。
シミュレーターには `ghcr.io/shima004/rcrs-server:v1.0.4` を使用します。[シミュレーターの起動仕様](simulator.md) も参照してください。

## 基本構造

| 項目 | 必須・省略時 | 内容 |
| --- | --- | --- |
| `apiVersion` | 必須 | `simworkflow/v1alpha1` |
| `kind` | 必須 | `Experiment` |
| `metadata.name` | 必須 | 実験名 |
| `metadata.description` | 任意 | 説明 |
| `spec.simulator.image` | 必須 | シミュレーターのイメージ |
| `spec.simulator.resources` | 必須 | シミュレーターのCPU・メモリ |
| `spec.simulator.parameters.timesteps` | 任意。マップの設定を使用 | 正の整数、最大2,147,483,647 |
| `spec.agents` | 1件以上 | エージェント定義。各定義の `name`・`image`・`resources` は必須 |
| `spec.agents[].runtime` | 任意。`java` | `java` または `python` |
| `spec.conditions.maps` | 1件以上 | `name` と登録済みの `versions` 配列、または直接参照の `source` |
| `spec.conditions.seeds` | 1件以上 | 重複のない整数配列 |
| `spec.execution.maxParallelRuns` | 必須 | 同時実行Pod数の上限。正の整数 |
| `spec.execution.timeout` | 必須 | 事前計算から本実行までの合計制限時間。`30m` など |
| `spec.execution.collectorResources` | 任意。1 CPU・256Mi | 結果収集用のCPU・メモリ |
| `spec.outputs.collect` | 1件以上 | `score`・`simulatorLogs`・`agentLogs` から選ぶ |

エージェント定義、マップ、シードを組み合わせて実行します。
`agents` は比較対象の一覧です。配列の全エージェントを1回のシミュレーションに同時投入する意味ではありません。
エージェント名は実験内で一意とします。登録マップは同じ名前でもバージョンが異なれば指定できます。

`spec.simulator.resources` と各 `spec.agents[].resources` に、割り当てるCPU・メモリを指定します。エージェントは種類ごとのコンテナに適用します。詳細は次節を参照してください。

サービスの接続先・認証情報は [接続設定](configuration.md) と [OSの資格情報ストア](credentials.md) で管理し、この実験YAMLには含めません。
マップの登録、`ref`・`versions: [latest]` の解決、SHA-256の固定、ステップ数の変更は [マップの運用](maps.md) を参照してください。
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

シミュレーターと各エージェントの両方でCPU・メモリが必須です。
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
`modules` には固定するモジュールを、`matrix.modules` には1つずつ切り替えるモジュールの候補を指定します。複数モジュールをまとめて切り替える場合は `matrix.groups` を使います。
モジュールのキーは差し替え箇所、値は実装の識別子を表す文字列です。Javaではクラス名、Pythonではインポートパスを指定します。

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
候補配列の空配列、重複する候補、空の実装識別子はエラーとします。`matrix` を指定する場合は、1つ以上のモジュール軸またはグループが必要です。

## 複数モジュールをセットで組み合わせる

`matrix.groups` は、グループ内のモジュールをまとめて選び、グループ間の直積を作ります。以下はエージェント定義内の抜粋です。キー・実装名は説明用です。

```yaml
matrix:
  groups:
    A:
      - name: a1
        modules:
          Agent.PathPlanning: impl.A1.PathPlanning
          Agent.Clustering: impl.A1.Clustering
      - name: a2
        modules:
          Agent.PathPlanning: impl.A2.PathPlanning
          Agent.Clustering: impl.A2.Clustering
    B:
      - name: b1
        modules:
          Agent.Search: impl.B1.Search
          Agent.TargetSelection: impl.B1.TargetSelection
      - name: b2
        modules:
          Agent.Search: impl.B2.Search
          Agent.TargetSelection: impl.B2.TargetSelection
```

この例は `A=a1/B=b1`、`A=a1/B=b2`、`A=a2/B=b1`、`A=a2/B=b2` の4構成です。a1のPathPlanningとa2のClusteringのように、セット内部の組み合わせが崩れることはありません。

- グループ名・候補名は1～63文字のASCII英数字・アンダースコア・ハイフンで、先頭は英字です。大文字・小文字は区別します。
- 各グループには1つ以上の候補が必要です。候補名はグループ内で一意とし、同一内容のセットの重複もエラーです。
- 各候補は1つ以上のモジュールを持ち、同じグループの全候補でモジュールキーを揃えます。1つだけのモジュールを持つセットも指定できます。
- 別グループ同士、固定の `modules`、単独の `matrix.modules` で同じモジュールキーは使えません。値が同じでも暗黙に上書きしません。
- 固定設定・各グループの選択・単独のモジュール軸を統合した設定を、既存のJava／Pythonワーカーへ渡します。候補の実装はイメージに含めてください。

グループ2候補 × グループ3候補 × 単独モジュール2候補なら、除外前は12構成です。さらにマップ数とシード数を掛けます。

選択名は実行計画の `configuration.groups`、モジュール生成の `manifest.json`、TUI詳細、`experiment results` へ残します。新しいワーカーは結果要約にも保持します。旧helperの結果要約に選択名がない場合、CLIの `results` は実行ID・構成IDを照合して保存済み計画から補完します。ダウンロードする要約自体は元ファイルなので、旧helperの結果と選択名を一緒に保存するには `--include plan,summary` を指定してください。

完全なPython向け定義と設定生成用の元設定は [grouped-modules.yaml](../examples/experiments/grouped-modules.yaml) と [grouped-python.yaml](../examples/module-configs/grouped-python.yaml) を参照してください。イメージ・マップを自分の環境へ合わせて変更します。

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

除外条件の `modules` には、`matrix.modules` またはグループ内に存在するモジュールキーと候補の値を指定します。固定の `modules` だけに存在するキーは使えません。
セット名で除外する場合は `groups` を指定します。

```yaml
exclude:
  - groups: {A: a1, B: b2}
```

この例は `A=a1` かつ `B=b2` だけを除外します。同じ除外条件で `groups` と `modules` を併記した場合も、両方が一致した場合に除外します。異なる除外条件同士はいずれかが一致すれば除外します。

空の除外条件、存在しないグループ・候補名・モジュールキー・候補値はエラーとします。
除外の結果、エージェントの構成が0件になった場合もエラーとします。

## マップの複数バージョン

登録マップは、1バージョンでも `versions` 配列で指定します。単数形の `version` は受け付けません。

```yaml
conditions:
  maps:
    - name: america04
      versions: [v1, v2, v3]
  seeds: [42, 7]
```

この例は同じ `america04` の3バージョンを指定順に展開し、各構成につき3バージョン × 2シードを実行します。実験全体のマップ数には、配列の行数ではなく展開後のバージョン数を使います。進捗・実行計画の一覧は `america04@v1` のように区別します。JSON実行計画・結果の各runには、展開後の単一の `version` を記録します。

空の `versions`、重複したバージョン、`source`／`sha256` との併記はエラーです。同じ名前・バージョンの重複も受け付けません。マップ登録と `latest` の扱いは [マップの運用](maps.md) を参照してください。

## 実行回数と結果

総実行回数は、次の式で求めます。

```text
全エージェントの除外後の構成数の合計 × マップ数 × シード数
```

サンプルはモジュール2候補 × 2候補から1構成を除外するため、3構成です。
マップ1件 × シード3件を掛け、合計9回のシミュレーションを実行する定義になります。
除外を省略すれば12回です。`maxParallelRuns` は総実行回数を変えません。

マップとシードはそれぞれ1件以上必要です。登録マップは名前とバージョンの組み合わせ、直接参照は名前、シードは値が重複するとエラーにします。
各シードで1回ずつ実行します。反復回数を別に指定する項目はありません。

各実行の結果には、実験を識別するID、エージェント名とイメージ、展開後のモジュール構成、シミュレータとエージェントそれぞれのCPU・メモリ設定、マップ、シード、実行状態を記録します。
同じ実験定義を再実行した場合も、実験の実行を識別できるようにします。
`simworkflow experiment plan -f FILE` で、展開後の構成と総実行回数を確認できます。
`--format json` でリソースやマップの参照先を含む実行計画を出力します。
実行件数による固定上限は設けず、組み合わせをすべて展開します。送信時には、定義サイズ・設定バンドル容量・署名付きURLの期限を検査します。

## 実行の制限と結果収集リソース

`maxParallelRuns` は同時実行Pod数の上限です。事前計算と本実行を含み、各実行では順番に起動します。`timeout` は事前計算Podの準備開始から本実行完了までの合計で、データ転送・段階間の待機・本実行の自動再試行も含みます。本実行の開始でリセットしません。

結果収集コンテナ `main` のCPU・メモリは `spec.execution.collectorResources` で指定します。省略時はCPU 1コア・メモリ256Miです。事前計算と本実行の両方に適用し、requestsとlimitsに同じ値を設定します。

```yaml
spec:
  execution:
    maxParallelRuns: 4
    timeout: 45m
    collectorResources: { cpu: 1, memory: 256Mi }
```

シミュレーター・エージェントの指定とは独立しています。同時実行数を増やす場合は、収集用CPU・メモリもPodの必要量に含めます。設定は計画に保存し、再送信・再現用ZIPにも引き継ぎます。

タイムアウトは正の秒数に相当する期間で指定します（例：`30m`・`2h`）。秒未満の端数は受け付けません。署名付きURLの期限から、送信時には実験全体の所要時間見積もりにも上限を設けます。[実行時の期限と容量制限](execution.md) を参照してください。

## 収集するスコアとログ

```yaml
spec:
  outputs:
    collect: [score, simulatorLogs, agentLogs]
```

| 値 | 内容 |
| --- | --- |
| `score` | 本実行の最終スコアを要約へ記録する |
| `simulatorLogs` | カーネル・サーバーのログと、SimScopeで再生する `rescue.log.7z` を収集する |
| `agentLogs` | 消防・警察・救急のエージェントログを収集する |

1件以上指定し、値の重複は避けてください。後から収集対象を変更しても送信済みの実験には反映されません。ログを省くと結果容量を減らせますが、失敗時の調査やSimScopeでの再生に必要なログは送信前に指定します。

要約・設定・事前計算データの保存と、Pod削除後に取得できる範囲は [実行と結果保存](execution.md#結果の保存) を参照してください。

## 検証と計画の識別子

未知の項目、重複したYAMLキー、複数のYAMLドキュメントはエラーとします。
実験名・エージェント名・マップ名は、小文字英数字とハイフンによる1–63文字とし、先頭と末尾は英数字にします。
モジュールキーと実装識別子には、ドット区切りの識別子を指定します。
CPUは引用符のない数値で指定し、JSON計画では精度を維持するため文字列で出力します。

グループ名の辞書順、単独モジュール軸名の辞書順で軸を並べ、それぞれの候補の記述順、マップの記述順、シードの記述順に展開します。
各構成に `config-...`、各実行に `run-...` のIDを付け、解決後の設定から生成します。
同じ計画は同じIDになります。グループの選択名もIDに含めるため、同じ実装でも選択名を変更するとIDが変わります。グループを使用しない既存定義のIDは変わりません。これらは実行計画上の識別子であり、再実行の履歴を区別する実行IDは送信・生成のたびに新しく発行します。

## エージェントイメージの準備

ADFの設定キーは [モジュール切り替え](adf-module-settings.md)、元設定の配置と起動契約は [Java](../templates/agents/java/README.md)／[Python](../templates/agents/python/README.md) を参照してください。
実行時にはイメージ内の元設定へ選択したモジュールだけを反映します。候補の実装はイメージに含めておきます。
マップZIPの形式、事前計算の順序、スコア・ログの保存先は [実行手順](execution.md) に記載しています。

モジュール固有のパラメータ指定、任意の起動コマンド、実験間の事前計算キャッシュ共有には対応していません。

[ドキュメント一覧へ](README.md)
