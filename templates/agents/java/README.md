# Javaエージェントのコンテナテンプレート

[adf-sample-agent-java](https://github.com/roborescue/adf-sample-agent-java) をベースにしたエージェント用です。
Java 21でビルドし、元の `launch.sh` で起動します。ビルドと起動に使うGradle・依存ライブラリはイメージに残します。
このテンプレート自体はエージェントのソースコードを含みません。

## 作成

1. このディレクトリの `Dockerfile`、`.dockerignore`、`simworkflow-runtime.gradle` を、エージェントリポジトリの `gradlew` があるディレクトリへコピーします。
2. 比較したいモジュールのJavaソースを `src/main/java` 以下に追加します。外部JARを使う場合は、元の `build.gradle` の依存設定に従って追加します。
3. 同じディレクトリで次のコマンドを実行します。

```sh
simworkflow agent build --repository my-team/agent-java --tag v1 --push .
```

登録先は設定済みの `REGISTRY_URL` を使います。リポジトリ名は自分のチームに合わせて変更します。Docker CLI・Buildxと稼働中のDockerデーモンが必要です。認証やビルドのみの使い方は [ビルド・登録ガイド](../../../docs/agent-build.md) を参照してください。表示されたdigest付きの `image:` を実験YAMLに指定します。
ビルド時にGradle本体と依存ライブラリを取得するため、ネットワーク接続が必要です。
`simworkflow-runtime.gradle` は `runtimeClasspath` も解決し、実行時に必要な推移依存をイメージ内に取得します。コンパイルのみでは、テストのないプロジェクトの実行時依存は取得されないことがあります。
元のサンプルは一部の依存に `master-SNAPSHOT` を使用しています。実験を再現する際には、依存バージョンとイメージのダイジェストを固定してください。

## コンテナの取り決め

| 項目 | パス・内容 |
| --- | --- |
| 作業ディレクトリ | `/app` |
| 元のモジュール設定 | `/app/config/module.cfg` |
| 元の起動設定 | `/app/config/launch.cfg` |
| 起動処理 | `/app/launch.sh -all` |
| 実験用設定の配置例 | `/experiment/module.cfg` |
| 実験用設定を選ぶ引数 | `-mc /experiment/module.cfg` |
| シミュレータ接続先の引数 | `-s simulator:27931` |

モジュール設定には、ロードできるクラス名を指定します。
`matrix` の候補をすべてイメージに含めれば、組み合わせごとの再ビルドは不要です。コードを変更した際は新しいイメージを作ります。

プラットフォームは元設定へ選択されたモジュールを反映し、`-mc` で指定して起動します。事前計算データは実験専用領域に保持し、本実行へ引き継ぎます。[実行手順](../../../docs/execution.md) を参照してください。
CPU・メモリの制限とJVMのヒープ設定は別です。元の `build.gradle` の `launch` タスクは `maxHeapSize = '2048m'` を指定しているため、変更が必要ならエージェント側のビルド設定も調整します。

公開サンプルでDockerビルドに成功し、生成した設定をADFの `ModuleConfig` で読み込んで基準実装のクラスをロードできることを確認しています。
Argo上で事前計算、データを読み込む本実行、結果のS3保存も確認しています。[検証記録](../../../docs/validation.md)を参照してください。

DijkstraとA*の2実装を追加し、事前計算の引き継ぎまで照合する例は [モジュール比較の検証サンプル](../../../examples/validation/module-comparison/README.md) を参照してください。
