# 失敗した実行の再送信

`experiment retry` は終了済みのWorkflowから失敗した実行を抽出し、新しいWorkflowへ送信します。各実行は「エージェント構成 × マップ × シード」の組み合わせです。

```sh
# 対象の確認のみ。S3への書き込みやArgoへの送信は行わない
simworkflow experiment retry --dry-run WORKFLOW_NAME

# 失敗分を再送信
simworkflow experiment retry WORKFLOW_NAME

# 並列数・各実行のタイムアウトを変更して再送信
simworkflow experiment retry \
  --max-parallel-runs 4 --timeout 2h \
  WORKFLOW_NAME

# 停止などで未実行・未完了になった組み合わせも含める
simworkflow experiment retry --include-unfinished WORKFLOW_NAME
```

オプションはWorkflow名より前に指定します。Argoが `Succeeded`・`Failed`・`Error` と判定した終了済みWorkflowが対象です。実行中なら終了を待つか、`experiment stop` で停止し、終了を確認します。

## 対象の選択と引き継ぐ設定

本実行の最後のPodが `Failed`・`Error` の実行と、事前計算が `Failed`・`Error` で本実行へ進めなかった実行を選びます。Podを作成する前に実行単位・段階のテンプレートが失敗した場合も対象です。Argo内の自動再試行で成功した実行は選びません。未実行・スキップ・本実行に進めなかった実行は `--include-unfinished` で含めます。対象がなければ追加送信しません。

指定したWorkflow自身の失敗分だけが対象です。再送信先のWorkflow名を指定すれば、その再送信で失敗した分をさらに再送信できます。元のWorkflowを繰り返し指定すると、元のWorkflowの失敗分を再び送信します。他の再送信ですでに成功したかどうかによる対象の除外は行いません。

保存済みの計画から、イメージのdigest、マップとチェックサム、モジュール・グループの選択、シード、CPU・メモリ、収集対象を引き継ぎます。手元のYAMLや更新後のイメージタグからは再構築しません。実行計画上のrun IDは維持し、別の実行ID・保存先を作成します。**事前計算から毎回やり直し**、署名付きURLも発行し直します。

並列数・タイムアウト・ServiceAccountは原則として元の設定を使います。`--service-account` でServiceAccountを変更できます。旧WorkflowにServiceAccountの記録がない場合は `simworkflow` を使います。タイムアウトを変更しても、署名付きURLの期限に基づく実験全体の見積もり上限は適用します。

実際の送信には、計画のハッシュとhelperのdigestを含む `reproduction.json`、digestで固定したエージェント・シミュレーター、マップのチェックサムが必要です。これらがない古い実験はYAMLから新しく送信してください。S3内の入力とクラスタからイメージを取得できる状態も維持します。

## 再送信分を含めて結果を取得する

```sh
simworkflow experiment results --with-retries ORIGINAL_WORKFLOW

simworkflow experiment download \
  --with-retries --include summary --stage simulation \
  ORIGINAL_WORKFLOW

simworkflow experiment download \
  --with-retries --file logs/simulation/rescue.log.7z --stage simulation \
  ORIGINAL_WORKFLOW
```

`--with-retries` を付けると、元の実験と、その実験から派生した再送信を対象にします。再送信先のWorkflow名を指定しても、元の実験を起点に同じ範囲を調べます。

各run IDについて、送信日時が新しい成功試行を採用します。成功試行がなければ、最も新しい試行を採用します。同じ日時なら保存先の実行IDの辞書順で決定します。成功判定はArgoの本実行の終了状態を使います。後続の試行が実行中・失敗でも、それ以前に成功した試行があればその結果を維持します。

たとえば元の100件中90件が成功し、失敗10件の再送信で8件が成功した場合、元の成功90件・再送信の成功8件・再送信でも失敗した2件を選びます。

要約・進捗・ログ・設定・事前計算データはすべて採用した同じ試行から取得します。要素が未保存でも別の試行から補完しません。`--include`・`--file`・`--run`・`--stage` の指定は通常のダウンロードと同じです。`plan.json` は元の実験全体の計画です。

`results --with-retries` は各要約に `sourceWorkflow`・`sourceExecutionId`・`attemptPhase` を追加します。`download.json` にはrun IDごとの採用試行と、各ファイルのWorkflow・実行ID・取得元のS3キーを記録します。統合結果の状態は全件成功なら `Succeeded`、未完了があれば `Incomplete`、それ以外に失敗があれば `Failed` です。元のArgo Workflowの状態は変更しません。

## ダウンロード済みファイルと履歴

統合ダウンロードの既定出力先は `artifacts/<指定したWorkflow名>-with-retries/` です。通常のダウンロード先と分けています。同じ採用試行のままであれば、同じ出力先へログなどを追加できます。

再送信の追加や完了によって採用試行が変わった場合、既存の出力先への追加はエラーにします。新しい `--output` を指定してください。以前の要約と別試行のログが混ざることや、ローカルファイルの上書きを防ぎます。

`--with-retries` を付けない `results`・`download` は、指定したWorkflowだけを扱います。`status` も指定Workflow単体の監視です。元の失敗結果はS3に残るので、元Workflowを直接指定して調査できます。`stop` も指定したWorkflowだけが対象です。

再送信の関係は各実行の `reproduction.json`、Argoの注釈、元の保存先の `retries/<execution-id>.json` に記録します。統合取得にはS3の一覧取得権限と、元のWorkflow・関連する再送信WorkflowをArgo APIから参照できることが必要です。統合取得を終えるまでは関連Workflowを削除しないでください。参照できない再送信がある場合、結果を黙って除外せずエラーにします。

Argoへの送信後にS3の再送信一覧の保存だけが失敗した場合、コマンドは送信済みのWorkflow名とエラーを表示します。同じコマンドをすぐに再実行すると重複送信になるため、そのWorkflowの状態を確認してください。そのWorkflow名を直接指定した `download --with-retries` では、明示指定した試行も取得対象に含めます。

再現用ZIPの `export` は引き続き指定Workflow単体が対象です。再送信の部分計画は `plan.json` に保存し、元の全組み合わせを記述したYAMLは同梱しません。別環境への `reproduce` は独立した新規実験になり、元環境の再送信一覧には接続しません。

[ドキュメント一覧へ](README.md)
