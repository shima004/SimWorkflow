# 実験をZIPで保存・再実行する

`experiment export` は、終了した実験の入力をコンテナイメージごとZIPへ保存します。`download` は結果の取得、`export` は別環境での再実行が目的です。Dockerは不要です。

## 保存する

```sh
simworkflow experiment export --output experiment.zip WORKFLOW_NAME

# 保存済みのサーバー・エージェントログも含める場合
simworkflow experiment export --include-logs --output experiment-with-logs.zip WORKFLOW_NAME
```

オプションはWorkflow名より前に置きます。`--output` の省略時は `WORKFLOW_NAME.zip` です。既存のファイルは上書きしません。対象は `Succeeded`・`Failed`・`Error` で終了したWorkflowです。

保存内容は次のとおりです。

| 内容 | ZIP内の場所 |
| --- | --- |
| 展開済みの実行計画（モジュール・グループ選択、マップ、シード、ステップ数、リソース、並列数、制限時間） | `plan.json` |
| 解決済みの実験YAML（送信時に保存されている場合） | `experiment.yaml` |
| シミュレーター・エージェント・helperの実際のイメージ | `images/`（OCI形式） |
| マップZIP | `maps/<sha256>.zip` |
| 取得できた段階別の要約・スコア、モジュール・サーバー設定、事前計算データ | `results/` |
| バージョン情報、入力の対応、ファイルのSHA-256、未取得の参考結果 | `manifest.json` |
| 再実行の案内 | `README.md` |

同じマップはSHA-256で、イメージとレイヤーはdigestで重複を除きます。ログは `--include-logs` を指定し、元の実験で収集されている場合に保存します。失敗した実験などで要約・事前計算データがない場合は、`missingResults` に記録します。再実行に必要なマップやイメージが不足している場合はZIPを完成させず、エラーにします。

CLIの接続設定・認証情報、署名付きURLを含むワーカー設定、元のWorkflowは保存しません。イメージの内容と、選択したログ・結果はそのまま保存します。

## 別環境で再実行する

復元先のArgo・S3・レジストリを [接続設定](configuration.md) に設定します。クラスタから復元先レジストリのイメージを取得できるようにしてください。

```sh
simworkflow experiment reproduce \
  --bundle experiment.zip \
  --name reproduced-experiment
```

最初にZIP全体のパス・サイズ・SHA-256、マップ、イメージの整合性をローカルで検査します。検査後、イメージを設定済みの `REGISTRY_URL` 配下の `simworkflow/reproduced` へ、マップを現在のS3バケットへ登録し、入力の参照先を置き換えてArgoへ送信します。途中で登録に失敗すると、登録済みの入力が残る場合があります。全入力の登録・確認が完了するまでWorkflowは送信しません。

GHCRなど所有者の指定が必要なレジストリでは、登録先リポジトリを指定します。

```sh
simworkflow experiment reproduce \
  --bundle experiment.zip \
  --name reproduced-experiment \
  --image-repository my-account/reproduced \
  --service-account simworkflow
```

元のS3・レジストリへの接続は不要です。元のrun IDと新しいrun IDの対応は、復元先の `reproduction.json` に保存します。ZIP内の事前計算データは比較・調査用です。実行IDや期限に結び付いているため再利用せず、事前計算と本実行の両方を実施します。

## 入力を確定する時点と制限

この機能を含むCLIの `submit`／`render` は、タグ指定のイメージを実際の `linux/amd64` イメージのdigestに解決します。マップも読み取り・検査して内容をS3へ保存し、計画と再現用情報を保存します。そのため、CLIから各イメージのレジストリへの読み取りと、S3への読み書きが必要です。レジストリ認証には設定済みの接続先の資格情報を使います。それ以外の接続先は匿名で取得します。

`validate`／`plan` は従来どおりオフラインです。入力の解決によってrun IDが変わる場合があるため、送信された実験の正確な計画はS3に保存された `plan.json` を参照してください。

タグの更新後も元のdigestが取得できれば保存できます。レジストリの削除・ガベージコレクションでイメージが失われる前にexportしてください。古い実験も、元のイメージがdigestで固定され、マップのハッシュが記録されていれば保存できます。当時の入力を証明できないタグ指定から、現在のイメージを代用することはありません。元のYAMLがない古い実験は、展開済み計画を使って再実行します。

対応プラットフォームは `linux/amd64` です。同じ入力を再実行しますが、CPU・実行時間制限・並行処理などの環境差によるスコアの完全一致までは保証しません。

イメージを含むためZIPは数GBになることがあります。保存時には作業用ファイルと完成ZIP、復元時には展開用の一時領域が必要です。上限は1ファイル16GiB、全体1TiB、10万ファイル、1マップ2GiB、計画1万実行です。ZIPはUnixでは0600、一時ディレクトリは0700で作成し、終了時に一時ファイルを削除します。

[ドキュメント一覧へ](README.md)
