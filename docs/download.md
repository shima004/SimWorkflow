# 実験結果のダウンロード

`experiment download` で、Workflowに保存された結果から必要な要素を取得できます。環境変数またはカレントディレクトリの `.env` を使用し、AWS CLIの設定は不要です。
オプションはWorkflow名より前に置きます。

```sh
# 全構成の本実行の要約だけ（スコアと実行条件を含む）
go run ./cmd/simworkflow experiment download \
  --include summary --stage simulation \
  --output artifacts/vc-scores \
  rescue-python-vc-comparison-5tmsf

# 本実行のシミュレーター・エージェントのログ
go run ./cmd/simworkflow experiment download \
  --include simulatorLogs,agentLogs --stage simulation \
  --output artifacts/vc-logs \
  rescue-python-vc-comparison-5tmsf

# 指定した構成のrescue.log.7zだけ
go run ./cmd/simworkflow experiment download \
  --run run-c4ec68cd352511cdacf9e2e85be04556 \
  --file logs/simulation/rescue.log.7z \
  --output artifacts/vc-rescue-log \
  rescue-python-vc-comparison-5tmsf

# 全構成の事前計算データZIPだけ
go run ./cmd/simworkflow experiment download \
  --include precompute --output artifacts/vc-precompute \
  rescue-python-vc-comparison-5tmsf
```

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
go run ./cmd/simworkflow experiment download \
  --run run-c4ec68cd352511cdacf9e2e85be04556 \
  --file logs/simulation/kernel.log \
  --file logs/simulation/agent-fire-brigade.log \
  --output artifacts/vc-selected-logs \
  rescue-python-vc-comparison-5tmsf
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
