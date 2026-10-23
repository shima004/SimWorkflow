# シミュレーターの実行仕様

使用するイメージは `ghcr.io/shima004/rcrs-server:v1.0.4` です。
実験サンプルにこのイメージを指定しています。
このページはイメージのメタデータと対応ソースを確認した記録で、Argoでの実行処理はまだ実装していません。

## イメージで確認した項目

| 項目 | 値 |
| --- | --- |
| プラットフォーム | `linux/amd64` |
| 実行ユーザー | `rcrs` |
| 作業ディレクトリ | `/opt/rcrs-server` |
| エントリーポイント | `java -jar /opt/rcrs-server/jars/rcrs-server-launcher.jar` |
| 既定の引数 | `comprun --nogui` |
| エージェント接続ポート | `27931/tcp` |
| 元のマップ | `/opt/rcrs-server/maps` |
| ログ保存用ボリューム | `/opt/rcrs-server/logs` |
| ソースのリビジョン | `7475b52ff49178f5f29c7c8b8e126bc1c5b3b395` |

## マップと設定の渡し方

起動引数の構成例です。`/experiment` 以下は実験ごとに準備するパスであり、イメージに元からあるパスではありません。

```text
comprun --nogui
  --map /experiment/scenario/map
  --config /experiment/scenario/config
  --log /experiment/logs
  --team comparison
```

`--map` は地理データのディレクトリ、`--config` は `kernel.cfg` を含む設定ディレクトリです。
マップの配布物には、この2つを用意する必要があります。
設定ファイルは `!include` で他のファイルを参照するため、必要なファイルを含む設定ディレクトリ全体を保持します。
マップのS3アーカイブについては、展開後のルートに `map/` と `config/` がある形式を想定しています。実際の配布物の形式は接続実装前に確認します。

元の `common.cfg` では `random.seed` を指定しています。
実験ごとのシードは、実行専用にコピーした設定へ反映し、すべての関連コンポーネントが同じ値を読むようにする必要があります。
確認したランチャーには `--seed` 引数はありません。

## 起動・終了とリソース

ランチャーはカーネルの接続待ちとシミュレーターの起動を行い、準備後に `Start your agents` を出力します。
エージェントはこの準備が終わった後に接続させる設計にします。
シミュレーター内のコンポーネントは `localhost` を使うため、同一のシミュレーターコンテナで起動します。
エージェントからは同じPod内なら `localhost`、別Podならサービスなどを介して接続します。実装方式は今後確定します。

複数のJVMが起動するため、コンテナのメモリ設定と各JVMのヒープ設定を区別します。
既定の最大ヒープの合計は、GUI・イベント記録を除いて6120MiBです。実験サンプルではシミュレーターのメモリを8Giに設定しています。
必要なら `--memory kernel=2g` などを繰り返して各プロセスのヒープを指定できますが、実験YAMLからの対応付けはまだ実装していません。
ログディレクトリは `rcrs` ユーザーから書き込める必要があります。

カーネル終了を待ってランチャーが終了します。成功判定やスコア抽出、タイムアウト時の終了、S3へのアップロードは今後実装します。

## 確認元

- [Dockerfile](https://github.com/shima004/rcrs-server/blob/7475b52ff49178f5f29c7c8b8e126bc1c5b3b395/Dockerfile)
- [起動引数](https://github.com/shima004/rcrs-server/blob/7475b52ff49178f5f29c7c8b8e126bc1c5b3b395/modules/launcher/src/rcrs/server/launcher/LauncherOptions.java)
- [起動・終了処理](https://github.com/shima004/rcrs-server/blob/7475b52ff49178f5f29c7c8b8e126bc1c5b3b395/modules/launcher/src/rcrs/server/launcher/RCRSLauncher.java)
- [各プロセスのヒープ設定](https://github.com/shima004/rcrs-server/blob/7475b52ff49178f5f29c7c8b8e126bc1c5b3b395/modules/launcher/src/rcrs/server/launcher/ProcessSpec.java)
- [共通設定とシード](https://github.com/shima004/rcrs-server/blob/7475b52ff49178f5f29c7c8b8e126bc1c5b3b395/maps/test/config/common.cfg)
