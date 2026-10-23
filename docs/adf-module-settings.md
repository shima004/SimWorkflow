# ADFサンプルのモジュール切り替え

提供されたJava版・Python版サンプルと、Python版の依存するADF Coreを確認した結果です。
コンテナを起動しての動作確認はまだ行っていません。

## 確認した設定方法

| 対象 | モジュール設定 | 設定ファイルの指定方法 |
| --- | --- | --- |
| Java | `config/module.cfg`。`キー : 実装クラス名` | `launch.sh` の `-mc [FILE]` |
| Python | `config/module.yaml`。ネストしたYAML | `config/launcher.yaml` の `adf.agent.moduleconfig.filename` |

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

PythonのModuleConfigはネストしたキーをドット区切りに平坦化します。
どちらのSampleRoadDetectorも `SampleRoadDetector.PathPlanning` と `SampleRoadDetector.Clustering` を参照しています。
単なる `pathPlanning` では、この差し替え箇所を指定できません。
同じPathPlanningでも、SearchやExtActionなどは別の設定キーを参照するため、1つのキーを変えて全箇所が変わるとは限りません。

## プラットフォームでの適用方針

実験YAMLの `modules` と `matrix.modules` では、ADFが実際に参照する設定キーを使います。
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

処理は以下の順序とします。

1. 対象イメージに対応する元のモジュール設定ファイルを取得する。
2. `matrix` を展開し、実行ごとに元の設定へ固定モジュールと選択された候補を上書きする。
3. 未指定のキーを残した完全な設定ファイルを生成する。
4. 実行ごとに独立したファイルとしてエージェントコンテナへ渡す。
5. Javaは `-mc` でそのファイルを指定する。Pythonは `launcher.yaml` の参照先へ配置するか、参照先を生成ファイルのパスへ変更する。
6. 使用した最終設定を実験結果とともに保存する。

Pythonサンプルの `main.py` は `config/launcher.yaml` を指定してLauncherを作成しているため、このサンプルではその設定に合わせてファイルを配置します。
確認したPython LauncherにはJavaの `-mc` に相当する引数はありません。
設定ディレクトリ全体を隠して他の設定ファイルを失わないよう、生成ファイルだけを配置する設計とします。

差し替え候補は、Javaではクラスパス、Pythonではインポートパスからロードでき、差し替え箇所に必要なインターフェースを満たす必要があります。
設定ファイル生成だけでは候補の実装コードを追加できません。候補を含むコンテナイメージが必要です。

ユーザー向けの [Javaコンテナテンプレート](../templates/agents/java/README.md) と [Pythonコンテナテンプレート](../templates/agents/python/README.md) では、作業ディレクトリを `/app` とし、元設定をそれぞれ `/app/config/module.cfg`、`/app/config/module.yaml` に含めます。
元設定の取得処理とJava/Python形式の選択フィールドは、今後のプラットフォーム実装で確定します。
`simworkflow experiment modules` で、ローカルの元設定から構成ごとの設定ファイルと `manifest.json` を生成できます。
`--agent` で対象を選び、`--base` に完全な元設定、`--format java|python` に設定形式、`--output-dir` に新規ディレクトリを指定します。
構成IDは実行計画と共通です。マップやシードごとに同一設定を複製することはしません。
元設定に存在しないキー、Javaの重複キー、Pythonの重複・曖昧なキーはエラーにします。
Pythonのアンカー・エイリアス・マージキーには現在対応していません。
コンテナからの元設定取得、自動配置、Argo Workflowによる実行は未実装です。
