# 実験結果のダウンロード

`experiment download` で、Workflowに保存された結果から必要な要素を取得できます。[ユーザー共通のconfig.yaml](configuration.md)、環境変数、カレントディレクトリの `.env` を使用でき、AWS CLIの設定は不要です。
`WORKFLOW_NAME` は送信時に返された名前、`RUN_ID` は対象の実行IDへ置き換えます。オプションはWorkflow名より前に置きます。

```sh
# 全構成の本実行の要約だけ（スコアと実行条件を含む）
simworkflow experiment download \
  --include summary --stage simulation \
  --output artifacts/vc-scores \
  WORKFLOW_NAME

# 本実行のシミュレーター・エージェントのログ
simworkflow experiment download \
  --include simulatorLogs,agentLogs --stage simulation \
  --output artifacts/vc-logs \
  WORKFLOW_NAME

# 指定した構成のrescue.log.7zだけ
simworkflow experiment download \
  --run RUN_ID \
  --file logs/simulation/rescue.log.7z \
  --output artifacts/vc-rescue-log \
  WORKFLOW_NAME

# 全構成の事前計算データZIPだけ
simworkflow experiment download \
  --include precompute --output artifacts/vc-precompute \
  WORKFLOW_NAME
```

## 再送信分を含める

```sh
simworkflow experiment download --with-retries --include summary --stage simulation WORKFLOW_NAME
```

元の実験と再送信の各run IDについて、最新の成功試行を採用し、成功がなければ最新の試行を採用します。要約・ログ・事前計算データなどは同じ試行から取得し、`--include`・`--file`・`--run`・`--stage` を引き続き使えます。取得元のWorkflow・実行IDは `download.json` に記録します。

既定の出力先は `artifacts/<Workflow名>-with-retries/` です。採用試行が変わった場合は新しい `--output` を指定します。詳細と保持が必要な情報は [再送信と結果の統合](retry.md) を参照してください。

## 選択できる要素

`--include` はカンマ区切りで複数指定できます。省略時は `all` です。

| 値 | 取得する内容 |
| --- | --- |
| `plan` | 実験全体の `plan.json`。構成ID・実行ID・モジュール・マップ・リソースなど |
| `summary` / `score` | 段階ごとの `summary.json`。スコアは本実行の要約に含まれます |
| `progress` | 段階ごとの最終保存済み `progress.json` |
| `simulatorLogs` | カーネル・サーバー・rescue.log.7zなどのログ |
| `agentLogs` | 種類別のエージェントログ（内部ログを含む） |
| `precompute` | 構成ごとの事前計算データ `data.zip` |
| `modules` | 解決後の種類別モジュール設定 |
| `config` | マップにシード・ステップ数を反映したシミュレーター設定 |
| `bundle` | 展開前の `bundle.tar.gz` |
| `all` | 上記すべて。バンドルと、そこから取り出したログ・設定を両方保存します |

`--stage precompute`、`--stage simulation`、`--stage all`（既定）で段階を選べます。`plan` は実験全体、`precompute` は段階の指定にかかわらず事前計算ZIPを取得します。
`--run` は完全なrun IDをカンマ区切りで指定し、省略時は全構成です。run IDは `experiment status` または `experiment results` で確認できます。

## ファイル単体の選択

`--file` はバンドル内の相対パスを完全一致で指定し、複数回指定できます。
`--file` だけを指定した場合は、指定ファイルのみを保存します。`--include` を明示すれば、要素指定と組み合わせられます。

```sh
simworkflow experiment download \
  --run RUN_ID \
  --file logs/simulation/kernel.log \
  --file logs/simulation/agent-fire-brigade.log \
  --output artifacts/vc-selected-logs \
  WORKFLOW_NAME
```

ログの指定では、パスの `precompute` / `simulation` に対応する段階だけを探します。
`--file modules/fireBrigade/module.yaml` のように段階名が入らないパスでは、`--stage` で対象を選べます。

現在のS3上の保存形式ではログ・設定はgzip圧縮したバンドルに入っています。ファイル単体の選択でも元のバンドルを転送する必要がありますが、選択したファイルだけを出力先へ保存します。同じバンドルは1回だけ取得し、作業用の一時ファイルは処理後に削除します。`rescue.log.7z` 自体の解凍は行いません。

## 出力と未取得のファイル

出力先の既定は `artifacts/<Workflow名>/` です。同じ実験の `download.json` がある出力先には、要素を追加して取得できます。たとえば要約の取得後、同じコマンドの `--include` を変えたり `--file` を指定したりすれば、同じフォルダにログを追加できます。
取得済みのファイルはマニフェストのサイズ・SHA-256と照合してスキップし、新しく取得したファイルをマニフェストの一覧へ統合します。別の実験の出力先、マニフェストのない既存フォルダ、ローカルで編集されたファイルは上書きせずエラーにします。取得済みの要約・進捗を最新の内容で取り直す場合は、別の `--output` を指定してください。

```text
<output>/
  download.json
  plan.json                         # 選択時
  <run-id>/
    precompute/
      summary.json                  # 選択時
      data.zip                      # 選択時
      logs/...
    simulation/
      summary.json                  # 選択時
      logs/kernel.log
      logs/rescue.log.7z
      modules/<role>/module.yaml
      scenario/config/...
```

ログのパスから `logs/<stage>/` の段階名を除き、各段階の `logs/` に配置します。ファイルとマニフェストは0600、作成するディレクトリは0700です。
`download.json` にWorkflowの状態、選択内容、取得元のS3キー、保存したファイルのサイズとSHA-256、未取得の項目を記録します。認証情報や署名付きURLは記録しません。ファイル一覧は過去の取得分も保持し、選択内容・未取得項目・追加件数・スキップ一覧・エラーは直近のコマンドの情報を記録します。

実行中でも、S3へ公開済みの結果を取得できます。ログは通常、各段階の終了後にバンドルとして公開されます。
`--include all` は存在する結果を保存し、未保存の項目をマニフェストへ記録します。明示的に選んだ要素・単体ファイルが存在しない場合は、取得できたファイルとマニフェストを残して終了コード1を返します。すべて未保存の場合も終了コード1です。
通信や破損アーカイブなどのエラーでも、保存済みの完全なファイルとエラーをマニフェストに残します。転送途中のファイルは削除します。

展開時はパスの逸脱・リンク・重複パスを拒否し、1バンドルの展開量を16GiB、エントリ数を100,000件までとします。

イメージ・マップ・実行計画も含めて別環境で再実行する場合は、[再現用ZIP](reproduction.md) の `experiment export` を使います。

[ドキュメント一覧へ](README.md)
