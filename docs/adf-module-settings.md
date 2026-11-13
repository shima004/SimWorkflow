# エージェントのモジュール切り替え

実験YAMLで指定したモジュールを、Java／Pythonエージェントの設定へ反映する方法です。候補となる実装はエージェントイメージに含めます。

## 設定ファイルとキー

| 対象 | モジュール設定 | 設定ファイルの指定方法 |
| --- | --- | --- |
| Java | `config/module.cfg`。`キー : 実装クラス名` | `launch.sh` の `-mc [FILE]` |
| Python | `config/module.yaml`。ネストしたYAML | `config/launcher.yaml` の `adf.agent.moduleconfig.filename` |

PythonのModuleConfigはネストしたキーをドット区切りに平坦化します。
どちらのSampleRoadDetectorも `SampleRoadDetector.PathPlanning` と `SampleRoadDetector.Clustering` を参照しています。
単なる `pathPlanning` では、この差し替え箇所を指定できません。
同じPathPlanningでも、SearchやExtActionなどは別の設定キーを参照するため、1つのキーを変えて全箇所が変わるとは限りません。

## 実行時の適用

実験YAMLの `modules`・`matrix.modules`・`matrix.groups.<グループ名>[].modules` では、ADFが実際に参照する設定キーを使います。
実験サンプルも `SampleRoadDetector.PathPlanning` などADFが参照するキーを使います。

Javaエージェントの指定例（エージェント定義の抜粋）:

```yaml
modules:
  SampleRoadDetector.Clustering: adf.impl.module.algorithm.KMeansClustering
matrix:
  modules:
    SampleRoadDetector.PathPlanning:
      - adf.impl.module.algorithm.DijkstraPathPlanning
      - my_team.module.algorithm.CustomPathPlanning
```

`my_team.module.algorithm.CustomPathPlanning` は独自実装の仮名です。サンプルに含まれているクラスではありません。
Pythonでも同じ設定キーを使えますが、候補の値にはPythonのインポートパスを指定します。
サンプルの値は `adf_core_python.implement.module.algorithm.a_star_path_planning.AStarPathPlanning` です。
JavaとPythonの実装クラス名を相互に置き換えることはしません。

複数のキーをセットで切り替える場合は [グループ指定](experiment-yaml.md#複数モジュールをセットで組み合わせる) を使用します。

実行時は、イメージ内の元設定に固定モジュールと選択した候補を反映し、指定していないキーは残します。Javaは生成したファイルを `-mc` で指定し、Pythonは `launcher.yaml` から生成ファイルを参照します。テンプレートに従ったイメージなら、この適用はワーカーが行います。使用した最終設定は実験結果にも保存します。

差し替え候補は、Javaではクラスパス、Pythonではインポートパスからロードでき、差し替え箇所に必要なインターフェースを満たす必要があります。
設定ファイル生成だけでは候補の実装コードを追加できません。候補を含むコンテナイメージが必要です。

ユーザー向けの [Javaコンテナテンプレート](../templates/agents/java/README.md) と [Pythonコンテナテンプレート](../templates/agents/python/README.md) では、作業ディレクトリを `/app` とし、元設定をそれぞれ `/app/config/module.cfg`、`/app/config/module.yaml` に含めます。
実行時にはエージェントコンテナ内で元設定を読み込み、`runtime: java|python` に従って生成・起動します。
送信前に設定を確認したい場合は、次の `experiment modules` を使います。接続設定やサービスへの通信は不要です。YAMLの `validate` だけでは、元設定にキーがあるか、候補の実装がイメージに含まれるかは確認しません。

## ローカルで設定ファイルを生成する

コマンド中の `examples/` は、このリポジトリのルートからのパスです。

実験YAMLと元設定から、構成ごとに設定ファイルを作成できます。マップやシードが異なっても、同じモジュール構成のファイルは1つだけ作成します。

```sh
# Javaエージェント: 3構成の.cfgとmanifest.jsonを生成
simworkflow experiment modules \
  -f examples/experiments/module-comparison.yaml \
  --agent comparison \
  --base examples/module-configs/java.cfg \
  --format java --output-dir /tmp/module-configs-java

# Pythonエージェント: 2構成の.yamlとmanifest.jsonを生成
simworkflow experiment modules \
  -f examples/experiments/python-module-comparison.yaml \
  --agent python-comparison \
  --base examples/module-configs/python.yaml \
  --format python --output-dir /tmp/module-configs-python
```

出力ディレクトリは新規のものを指定します。既存のディレクトリは上書きしません。
元設定に存在しないキー、Javaの重複キー、Pythonの重複・曖昧なキーはエラーです。Pythonのアンカー・エイリアス・マージキーには対応していません。
`manifest.json` には各ファイルと、実行計画に共通する構成ID・エージェント名・イメージ・モジュール設定・リソース設定を記録します。
同梱の元設定は生成処理を試すための最小例です。実際の実験では自分のエージェントの完全な設定ファイルを `--base` に指定してください。
`my_team` や `src.my_team` の実装は例示用なので、実際にイメージに含めた実装に置き換えます。
生成ファイルをコンテナに渡す手順は [Java](../templates/agents/java/README.md)／[Python](../templates/agents/python/README.md) のテンプレートを参照してください。

グループの設定生成は次の例で試せます。4構成の設定と、選択したグループ名を含むマニフェストを作ります。

```sh
simworkflow experiment modules \
  -f examples/experiments/grouped-modules.yaml \
  --agent grouped-python \
  --base examples/module-configs/grouped-python.yaml \
  --format python --output-dir /tmp/grouped-module-configs
```

## 参考：確認したサンプルとソース

以下は設定方式を確認した時点のソースです。Pythonのuvテンプレートの対象・依存バージョンは [Pythonテンプレート](../templates/agents/python/README.md) を参照してください。

Javaエージェントの根拠:

- [module.cfg](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/config/module.cfg)
- [launch.sh](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/launch.sh)
- [SampleRoadDetector.java](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/src/main/java/sample_team/module/complex/SampleRoadDetector.java)

Pythonエージェントの根拠:

- [module.yaml](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/config/module.yaml)
- [launcher.yaml](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/config/launcher.yaml)
- [main.py](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/main.py)
- [SampleRoadDetector](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/src/sample_team/sample_road_detector.py)
- [ADF CoreのModuleConfig](https://github.com/adf-python/adf-core-python/blob/c5eaf8d2dae07c0bb1a3227efca9effd4fca110b/src/adf_core_python/core/agent/config/module_config.py)

[ドキュメント一覧へ](README.md)
