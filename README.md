# SimWorkflow

RoboCup Rescue Simulationの比較実験を、YAMLで定義してArgo Workflows上で実行するCLIです。モジュールの組み合わせ・マップ・シードから実行計画を作り、事前計算、本実行、進捗確認、結果のダウンロードまで扱います。

## できること

- 設定済みレジストリへエージェントイメージをビルド・登録する
- エージェントのモジュール候補や、複数モジュールをまとめたセットを組み合わせて比較する
- シミュレーターと消防・警察・救急ごとのCPU・メモリを指定する
- 事前計算データをS3へ保存し、別ステップの本実行へ引き継ぐ
- TUIで実行を選択し、進捗・スコア・設定詳細を確認する
- ログ・要約・事前計算データなど、必要な結果だけを取得する

SimScopeでのリアルタイム表示・保存ログ再生も利用できます。SimScopeは任意機能で、接続先を設定した場合に使います。

## はじめる

1. [GitHub Releases](https://github.com/shima004/SimWorkflow/releases) からCLIをダウンロードし、PATHに配置します。[インストール手順](docs/installation.md) を参照してください。
2. `simworkflow config init` で共通設定を作り、Argo・S3・レジストリの接続先を入力し、認証情報はOSの資格情報ストアへ登録します。[接続設定](docs/configuration.md) に書式をまとめています。
3. エージェントイメージとマップを用意し、[最初の実験](docs/quickstart.md) に沿って実行します。

```sh
simworkflow --version
simworkflow config init
simworkflow config path
# 表示されたconfig.yamlを編集（認証情報は書かない）
simworkflow credentials set s3
# Argo・レジストリに認証が必要なら、それぞれ登録
simworkflow config check --connect
```

実験には、Argo Workflowsを導入したKubernetesクラスタ、S3互換サービス、実行ノードから取得できるコンテナイメージが必要です。クラスタの準備は [実行環境の準備](docs/distribution.md) を参照してください。CLIの起動にGoやDockerは不要です。`agent build` を使う場合はDockerが必要です。

## 実験の基本操作

以下は、自分の環境に合わせた `experiment.yaml` を用意した後の操作です。`WORKFLOW_NAME` は送信時に表示された名前へ置き換えます。

```sh
simworkflow experiment validate -f experiment.yaml
simworkflow experiment plan -f experiment.yaml
simworkflow experiment submit -f experiment.yaml
simworkflow experiment list
simworkflow experiment status --watch WORKFLOW_NAME
simworkflow experiment results WORKFLOW_NAME
simworkflow experiment download --include summary --stage simulation WORKFLOW_NAME
```

`validate` と `plan` は接続設定なしで利用できます。`submit` からは設定済みのサービスへアクセスします。実行時には公開済みhelperイメージを使用し、ワーカー設定はデフォルトでS3へ保存します。

## ドキュメント

| 目的 | ガイド |
| --- | --- |
| 最初の実験を動かす | [クイックスタート](docs/quickstart.md) |
| モジュール単位・セット単位の比較やリソースを定義する | [実験YAMLの仕様](docs/experiment-yaml.md) |
| エージェントイメージを作る | [ビルド・登録](docs/agent-build.md)・[Python（uv／pip）](templates/agents/python/README.md)・[Java](templates/agents/java/README.md) |
| 認証情報を安全に保存する | [OSの資格情報ストア](docs/credentials.md) |
| マップを登録・管理する | [マップ](docs/maps.md) |
| 実験を一覧表示・停止・監視し、結果を取得する | [進捗・スコア](docs/progress.md)・[ダウンロード](docs/download.md) |
| 開発・リリースする | [開発ガイド](docs/development.md) |

[ドキュメント一覧](docs/README.md) に、接続トラブル、実行の仕組み、SimScope連携、検証記録もまとめています。
