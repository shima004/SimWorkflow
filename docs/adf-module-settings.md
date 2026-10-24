# エージェントのモジュール切り替え

実験YAMLで指定したモジュールを、Java／Pythonエージェントの設定へ反映する方法です。候補となる実装はエージェントイメージに含めます。
テンプレートのコンテナとArgo上での動作確認結果は [検証記録](validation.md) を参照してください。

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

Java版の指定例（エージェント定義の抜粋）:

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

処理は以下の順序です。

1. 対象イメージに対応する元のモジュール設定ファイルを取得する。
2. `matrix` を展開し、実行ごとに元の設定へ固定モジュールと選択された候補を上書きする。
3. 未指定のキーを残した完全な設定ファイルを生成する。
4. 実行ごとに独立したファイルとしてエージェントコンテナへ渡す。
5. Javaは `-mc` でそのファイルを指定する。Pythonは `launcher.yaml` の参照先へ配置するか、参照先を生成ファイルのパスへ変更する。
6. 使用した最終設定を実験結果とともに保存する。

Pythonサンプルの `main.py` は `config/launcher.yaml` を指定してLauncherを作成しているため、このサンプルではその設定に合わせてファイルを配置します。
確認したPython LauncherにはJavaの `-mc` に相当する引数はありません。
設定ディレクトリ全体を隠して他の設定ファイルを失わないよう、生成ファイルを独立した場所へ配置します。

差し替え候補は、Javaではクラスパス、Pythonではインポートパスからロードでき、差し替え箇所に必要なインターフェースを満たす必要があります。
設定ファイル生成だけでは候補の実装コードを追加できません。候補を含むコンテナイメージが必要です。

ユーザー向けの [Javaコンテナテンプレート](../templates/agents/java/README.md) と [Pythonコンテナテンプレート](../templates/agents/python/README.md) では、作業ディレクトリを `/app` とし、元設定をそれぞれ `/app/config/module.cfg`、`/app/config/module.yaml` に含めます。
実行時にはエージェントコンテナ内で元設定を読み込み、`runtime: java|python` に従って生成・起動します。
`simworkflow experiment modules` で、ローカルの元設定から構成ごとの設定ファイルと `manifest.json` を生成できます。
`--agent` で対象を選び、`--base` に完全な元設定、`--format java|python` に設定形式、`--output-dir` に新規ディレクトリを指定します。
構成IDは実行計画と共通です。マップやシードごとに同一設定を複製することはしません。
元設定に存在しないキー、Javaの重複キー、Pythonの重複・曖昧なキーはエラーにします。
Pythonのアンカー・エイリアス・マージキーには現在対応していません。
Argo上でも同じ設定生成処理を使用します。[事前計算付き実験の実行](execution.md) を参照してください。

## ローカルで設定ファイルを生成する

コマンド中の `examples/` は、このリポジトリのルートからのパスです。

実験YAMLと元設定から、構成ごとに設定ファイルを作成できます。マップやシードが異なっても、同じモジュール構成のファイルは1つだけ作成します。

```sh
# Java版: 3構成の.cfgとmanifest.jsonを生成
simworkflow experiment modules \
  -f examples/experiments/module-comparison.yaml \
  --agent comparison \
  --base examples/module-configs/java.cfg \
  --format java --output-dir /tmp/module-configs-java

# Python版: 2構成の.yamlとmanifest.jsonを生成
simworkflow experiment modules \
  -f examples/experiments/python-module-comparison.yaml \
  --agent python-comparison \
  --base examples/module-configs/python.yaml \
  --format python --output-dir /tmp/module-configs-python
```

出力ディレクトリは新規のものを指定します。既存のディレクトリは上書きしません。
`manifest.json` には各ファイルと、実行計画に共通する構成ID・エージェント名・イメージ・モジュール設定・リソース設定を記録します。
指定していない元設定は残し、元設定に存在しないキーへの上書きはエラーにします。
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

以下は設定方式を確認した時点のソースです。Python uv版テンプレートの対象・依存バージョンは [Pythonテンプレート](../templates/agents/python/README.md) を参照してください。

Java版の根拠:

- [module.cfg](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/config/module.cfg)
- [launch.sh](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/launch.sh)
- [SampleRoadDetector.java](https://github.com/roborescue/adf-sample-agent-java/blob/e9339b10a5188ae15747fba836b6ea035b3b570d/src/main/java/sample_team/module/complex/SampleRoadDetector.java)

Python版の根拠:

- [module.yaml](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/config/module.yaml)
- [launcher.yaml](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/config/launcher.yaml)
- [main.py](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/main.py)
- [SampleRoadDetector](https://github.com/adf-python/adf-sample-agent-python/blob/042a3477fb60f388e277e691dc2035e1a6555807/src/sample_team/sample_road_detector.py)
- [ADF CoreのModuleConfig](https://github.com/adf-python/adf-core-python/blob/c5eaf8d2dae07c0bb1a3227efca9effd4fca110b/src/adf_core_python/core/agent/config/module_config.py)

[ドキュメント一覧へ](README.md)
