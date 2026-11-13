# SimWorkflow 利用ガイド

SimWorkflowは、RoboCup Rescue Simulationの比較実験をYAMLで定義し、Argo Workflows上で実行するCLIです。エージェント構成・マップ・シードの組み合わせを作り、事前計算、本実行、進捗確認、結果取得まで扱います。

## 初めて使う

既存の実行環境を使う場合は、次の順に進めてください。

1. [インストール](installation.md)：OS・CPUに合うCLIを配置する。
2. [接続設定](configuration.md)：管理者から受け取ったArgo・S3・レジストリの接続先を設定する。
3. [認証情報の管理](credentials.md)：OSの資格情報ストアへ認証情報を登録する。
4. [最初の実験](quickstart.md)：1構成・1マップ・1シードで実行し、結果を取得する。

実験にはKubernetes・Argo Workflows、S3互換サービス、コンテナイメージを取得できるレジストリが必要です。実行環境から用意する場合は、管理者向けの [実行環境の準備](distribution.md) を参照してください。CLIの起動にはGoやDockerは不要です。エージェントイメージを作る場合はDockerを使います。

## 目的から探す

| 目的 | ガイド |
| --- | --- |
| CLIをインストール・更新する | [インストール](installation.md) |
| 接続先や設定の優先順位を調べる | [接続設定](configuration.md) |
| 認証情報を登録・変更する、CIから使う | [認証情報の管理](credentials.md) |
| エージェントイメージを作って登録する | [ビルド・登録](agent-build.md)・[Java](../templates/agents/java/README.md)・[Python](../templates/agents/python/README.md) |
| マップを登録・一覧表示する | [マップ](maps.md) |
| 比較条件やCPU・メモリを指定する | [実験YAML](experiment-yaml.md) |
| モジュールを切り替える、設定をローカルで確認する | [モジュール切り替え](adf-module-settings.md) |
| 実験を一覧表示・停止する、進捗とスコアを見る | [実験の管理・進捗](progress.md) |
| スコア・ログなど必要な結果を取得する | [ダウンロード](download.md) |
| 失敗分を再送信し、結果をまとめる | [再送信と結果の統合](retry.md) |
| 実験を別環境で再実行するために保存する | [再現用ZIP](reproduction.md) |
| シミュレーションをブラウザで見る | [SimScope連携（任意）](simscope.md) |
| 接続や実行の問題を調べる | [トラブル対応](connections.md) |
| 実行順序・保存先・制限を調べる | [実行と結果保存](execution.md) |

## このガイドの読み方

コマンド例は、PATHに配置した `simworkflow` を使います。`registry.example.com` などの接続先やイメージ名は、自分の環境の値へ置き換えてください。`examples/` を使う例は、このリポジトリのルートから実行します。

```sh
# 全体オプション → コマンド → コマンドのオプション → Workflow名
simworkflow --config ./config.local.yaml experiment status --watch WORKFLOW_NAME
```

`--config`・`--env-file` はコマンドより前、`--watch` などのコマンド固有のオプションはWorkflow名などの位置引数より前に置きます。各コマンドの `--help` でオプションを確認できます。接続するコマンドは、ヘルプ表示時にも接続設定が必要です。

| 用語・表記 | 意味 |
| --- | --- |
| 実験 | YAMLに定義した比較条件全体 |
| 構成 | エージェント定義に、固定モジュールと選択した候補を適用したもの |
| 実行（run） | 1構成 × 1マップバージョン × 1シード。事前計算と本実行の2段階からなる |
| `WORKFLOW_NAME` | 送信時に表示されるArgoのWorkflow名。監視・結果取得・停止に指定する |
| `RUN_ID` / run ID | `run-...` 形式の実行計画上の識別子。`status`・`results` で確認し、結果の絞り込みに使う |
| 実行ID（execution ID） | 送信ごとに発行する実験全体の識別子。再送信でも新しくなり、S3の保存先を分ける |
| helper（ワーカー） | 実行Pod内で起動管理・データの受け渡し・結果保存を行うイメージ。通常はCLIの既定値を使う |
| TUI | ターミナル内でキー操作できる画面 |

`validate`・`plan`・`modules` は接続設定なしで使えます。`render` はArgoへ送信しませんが、イメージの取得やS3への保存を行います。送信前に条件だけを確認したい場合は `plan` を使ってください。

## 管理者・開発者向け

| 目的 | ガイド |
| --- | --- |
| クラスタ・S3・レジストリ・権限を用意する | [実行環境の準備](distribution.md)・[Helm Chart](../charts/simworkflow/README.md) |
| 改造したシミュレーターを使う | [カスタムシミュレーター](../templates/simulators/README.md)・[起動仕様](simulator.md) |
| ソースからビルド・検証する、CLI・helperを公開する | [開発ガイド](development.md) |

定義のサンプルは [`examples/experiments/`](../examples/experiments/) にあります。イメージ・マップ・独自モジュールの参照は、自分の環境に合わせて変更してください。

[プロジェクトのREADMEへ戻る](../README.md)
