# 改造したシミュレーターを使う

自分でスコア関数やシミュレーション処理を変更したRCRSサーバーを、SimWorkflowの実験で使うためのガイドです。改造したサーバーをビルドし、[Dockerfile](Dockerfile) でイメージにまとめ、レジストリへ登録して実験YAMLから指定します。

このテンプレートは、RCRSサーバーの構成とエージェント接続方式を維持する改造が対象です。事前計算・本実行の制御や接続プロトコルも変更した場合は、ランチャーやエージェント側との対応も必要です。

## 1. 改造後のサーバーを用意する

自分のサーバーリポジトリの手順でビルドし、実行に必要なファイルを次の構成にまとめます。ビルドタスク名や依存バージョンは、そのリポジトリで指定したものを使います。

```text
my-simulator-build/                  # Dockerのビルドコンテキスト
  rcrs-server/
    jars/                           # 改造したサーバーのビルド済みJAR
      kernel.jar
      rescuecore2.jar
      standard.jar
      gis2.jar
      ...
    lib/                            # そのサーバーに対応する依存ライブラリ
    scripts/                        # 設定を探索する作業ディレクトリ
    log4j.properties
    LICENSE
```

`jars/` と `lib/` には、同じビルドに対応する一式を含めます。例えば独自スコア関数を追加した場合、ビルド後の `standard.jar` などにそのクラスと依存クラスが入っていることを確認します。マップ設定の `score.function` には、そのクラス名を指定します。

マップは [マップ登録](../../docs/maps.md) で別途管理します。実行時にはマップと設定が `/work/scenario/` へ展開されるため、実験用マップをイメージに収録する必要はありません。Dockerfileはランチャーのルート検出に必要な空の `maps/` も作成します。配布物の `scripts/` はそのまま含めます。

## 2. CLIが使う起動方法に合わせる

CLIのワーカーは、シミュレーターコンテナで次のコマンドを直接起動します。事前計算の場合の例です。

```sh
java -jar /opt/rcrs-server/jars/rcrs-server-launcher.jar precompute \
  --nogui --map /work/scenario/map --config /work/scenario/config \
  --log /work/logs/precompute --team my-team
```

本実行ではモードを `comprun`、ログ保存先を `/work/logs/simulation` にして、別Podで起動します。ワーカーがコマンドを指定するため、イメージの `ENTRYPOINT` だけを独自の起動スクリプトへ変えても、そのスクリプトは実験時には使われません。

| 項目 | 条件 |
| --- | --- |
| プラットフォーム | `linux/amd64` |
| サーバー配置 | `/opt/rcrs-server` |
| ランチャー | `jars/rcrs-server-launcher.jar` が上記のモード・引数に対応する |
| エージェント接続 | 同じPodから `localhost:27931` へ接続できる |
| 準備完了 | 接続可能になったら標準出力へ `Start your agents` を出力する |
| 書き込み先 | 実行ユーザーが `/work/scenario/config` と指定されたログディレクトリへ書き込める |
| 終了処理 | 本実行のカーネル終了まで待ち、起動した子プロセスも終了できる |

テンプレートは公開イメージから対応済みランチャーとJavaランタイムを使い、サーバー本体を自分の配布物で置き換えます。ランチャーの参照先はdigestで固定しています。Javaやランチャーとの互換性が必要な改造では、Dockerfileを自分の実装に合わせて変更してください。

## 3. ビルドして登録する

以下はSimWorkflowリポジトリのルートから実行する例です。`my-simulator-build` の場所は自分のビルドコンテキストへ置き換えます。

```sh
simworkflow agent build \
  --repository my-team/simulator --tag v1 \
  --dockerfile templates/simulators/Dockerfile \
  --push ./my-simulator-build
```

専用の `simulator build` コマンドはありません。現在の `agent build` はDockerfileのビルド・登録処理なので、シミュレーターにも使えます。登録先は設定済みの `REGISTRY_URL` です。認証やコマンドの詳細は [ビルド・登録](../../docs/agent-build.md) を参照してください。ソースから実行する場合は、先頭の `simworkflow` を `go run ./cmd/simworkflow` に置き換えます。

ローカルビルドだけなら `--push` を省略します。大きいレイヤーのアップロードでタイムアウトする場合は、コマンドの先頭に `HTTP_TIMEOUT=15m` を付けるなど、転送に必要な時間を指定します。

Dockerだけでビルドする場合は次の形です。

```sh
docker build --platform linux/amd64 \
  -f templates/simulators/Dockerfile \
  -t registry.example.com/my-team/simulator:v1 ./my-simulator-build
docker push registry.example.com/my-team/simulator:v1
```

この場合のログインやHTTPレジストリへの対応はDocker側の設定に従います。

## 4. 実験YAMLから指定する

`agent build --push` が最後に表示する `image:` のdigest付き参照を、既存の実験YAMLの次の箇所へ指定します。

```yaml
spec:
  simulator:
    image: registry.example.com/my-team/simulator@sha256:実際に表示されたdigest
    resources: { cpu: 2, memory: 8Gi }
    parameters: { timesteps: 5 }
  outputs:
    collect: [score, simulatorLogs, agentLogs]
```

実験ノードからもそのイメージを取得できる必要があります。イメージの参照先を変更した新しい実験を送信してください。既存Workflowや `experiment retry` は保存済みのイメージを使うため、その参照先は変わりません。helperは引き続きSimWorkflow用のものを使います。

## 5. 小さい実験で確認する

まずはマップ1件・シード1件・少ないステップ数・並列1で送信し、次を確認します。

1. 事前計算サーバーが待受を開始し、各種類のエージェントが接続できる。
2. 事前計算が終了してデータが保存される。
3. 別Podの本実行がそのデータを読み込んで開始する。
4. 正常終了し、要約とスコア・ログを取得できる。

検証時は `simulatorLogs` と `agentLogs` を収集します。独自のクラスがイメージに含まれていない場合や、サーバーと設定のバージョンが合わない場合は、エージェント接続前にカーネルが終了することがあります。保存した `server.log`・`kernel.log` を確認してください。

## 独自スコア・ログ・メモリの扱い

代表スコアの取得には、本実行の `kernel.log` に出力される `Score:` を使います。独自の集計ファイルを作る場合は、`--log` で指定された段階別ログディレクトリへ保存し、`outputs.collect` に `simulatorLogs` を含めます。ファイルを保存しただけでは、独自の指標がCLIのスコア列へ自動追加されるわけではありません。

イメージ内の固定パスへログを書く実装では、`--log` で指定された段階別ログディレクトリへ出力するように変更してください。

`spec.simulator.resources` はコンテナのCPU・メモリ指定です。複数JVMのヒープ上限はランチャー側の設定なので、改造によって使用量が変わる場合は両方を合わせて確認します。詳しくは [シミュレーターの実行仕様](../../docs/simulator.md) を参照してください。
