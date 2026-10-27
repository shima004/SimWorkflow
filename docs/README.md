# ドキュメント

初めて使う場合は、インストール → 接続設定 → クイックスタートの順に進めてください。コマンド例はPATHに配置した `simworkflow` を使用します。ソースからの実行方法は開発ガイドに記載しています。

## 利用する

| ガイド | 内容 |
| --- | --- |
| [インストール](installation.md) | OS・CPUに合う配布物の選択、チェックサム、PATH、self-update |
| [接続設定](configuration.md) | 共通YAML、環境変数、.env、優先順位、設定項目一覧 |
| [認証情報](credentials.md) | OSストアへの登録・削除、CI、既存ファイルからの移行 |
| [クイックスタート](quickstart.md) | マップの登録から実験の送信・監視・結果取得まで |
| [実験YAMLの仕様](experiment-yaml.md) | 単独モジュール・セットの組み合わせ、除外、マップ・シード、CPU・メモリ |
| [モジュール切り替え](adf-module-settings.md) | ADFの設定キー、元設定への反映、ローカルでの設定生成 |
| [マップ](maps.md) | ZIPの形式、名前・バージョン、登録、同時登録の条件 |
| [実験の管理・進捗・スコア](progress.md) | 一覧・停止、TUIの操作、設定詳細、テキスト監視、結果要約 |
| [失敗分の再送信](retry.md) | 対象の確認、再送信、試行を統合した結果取得 |
| [ダウンロード](download.md) | 要素・実行・段階・ファイルを選んで取得、追加取得 |
| [再現用ZIP](reproduction.md) | イメージ・マップ・計画の保存、別環境への復元・再実行 |
| [SimScope連携（任意）](simscope.md) | URLの設定、実行中の接続、保存ログの再生 |
| [接続トラブル](connections.md) | 接続確認の範囲、Argoの403、S3の404、レジストリへの到達性 |

## エージェント・実行環境を準備する

| ガイド | 内容 |
| --- | --- |
| [ビルド・登録](agent-build.md) | REGISTRY_URLを使ったagent build、認証、実験YAML用のimage出力 |
| [Pythonエージェント](../templates/agents/python/README.md) | uv／pipでのイメージ作成、依存関係、設定ファイルの配置 |
| [Javaエージェント](../templates/agents/java/README.md) | イメージ作成、Gradle依存の同梱、設定ファイルの配置 |
| [実行環境の準備](distribution.md) | Argo・S3・レジストリ・RBAC、公開helper、独自helper |
| [実行の仕組み](execution.md) | 事前計算と本実行、S3保存、失敗・再試行、期限・サイズ制限 |
| [シミュレーターの仕様](simulator.md) | 対象イメージ、起動引数、接続先、JVMメモリ |

## 開発・検証する

| ガイド | 内容 |
| --- | --- |
| [開発ガイド](development.md) | ソースからのビルド、検証コマンド、実装の構成、Release公開 |

[`examples/experiments/`](../examples/experiments/) は定義を作るためのサンプルです。イメージ・マップ参照は自分の環境へ合わせて変更します。

[プロジェクトのREADMEへ戻る](../README.md)
