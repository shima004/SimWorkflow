# 進捗・スコアの確認

`WORKFLOW_NAME` は `experiment submit` が返した名前です。オプションはWorkflow名より前に置きます。

```sh
simworkflow experiment list
simworkflow experiment status WORKFLOW_NAME
simworkflow experiment status --watch WORKFLOW_NAME
simworkflow experiment status --watch --plain WORKFLOW_NAME
simworkflow experiment results WORKFLOW_NAME
```

## シミュレーション終了後の処理

一覧の `ACTIVITY` 列で、結果収集コンテナ `main` の処理を確認できます。TUI・`--plain`・通常のstatusで共通です。

| ACTIVITY | 処理 |
| --- | --- |
| Reading score | ログ末尾から最終スコアを取得 |
| Compressing | 結果または事前計算データを圧縮 |
| Uploading precompute | 事前計算データをS3へ保存 |
| Uploading results | 結果バンドルをS3へ保存 |
| Uploading summary | 要約をS3へ保存 |
| - | 実行中、完了、または未対応helper |

シミュレーターが終了しても、結果保存が完了するまではSTATEがRunningのままです。短い処理は画面の更新間に通過することがあります。各処理への切り替えはmainの標準出力にも記録します。

この表示とスコア取得の高速化には対応済みhelperイメージが必要です。CLIだけを更新しても、既存Podや指定済みhelperの処理は変わりません。mainのリソース指定は [実験YAML](experiment-yaml.md) を参照してください。

## STATEの件数と推定時間

`status --watch` は画面上部に、実行一覧全体のSTATE別件数と推定時間を固定表示します。画面外の行も集計に含めます。狭い端末では折り返して表示し、その下の実行一覧をスクロールできます。`--plain` や通常の `status` にも同じ集計を表示します。

```text
STATE (total 100): Pending:70 Running:10 Succeeded:18 Failed:2 Error:0 Not run:0
Estimate: remaining ~34m0s | total ~52m0s | samples:28 parallel:10
```

- STATEの件数は、各行に表示された現在の段階の状態をそのまま集計します。`Succeeded` に事前計算完了の行が含まれる場合もあります。推定時間では、その行の本実行を残作業として扱います。
- `remaining` は観測時点からの推定残り時間、`total` はWorkflow開始から完了までの推定合計時間です。`samples` は速度の集計に使った実行数、`parallel` は推定に使う現在の同時実行数です。
- 監視更新間の完了ステップ差分と時間差から、実行ごとの1ステップの所要時間を計測します。新しい観測を30%、従来の速度を70%で反映します。ステップが進まない更新だけでは、速度を速く見積もり直しません。
- 未開始の実行には、観測済みのステップ速度と事前計算時間の中央値を使います。完了した本実行の所要時間・ステップ数も速度の参考にします。残作業時間を実際にRunningの行数（`maxParallelRuns` 以下）で割り、最長の1実行より短くならないように見積もります。
- 最初の数回で速度が得られない場合は `collecting step samples`、必要な事前計算時間や総ステップ数が不明な場合も理由を表示して推定を保留します。終了したWorkflowには `finished` を表示します。

推定値は `execution.timeout` から算出する制限時間ではありません。マップ・エージェント構成の違い、ノードの空き待ち、初期化、転送、再試行などにより変動する概算です。新しいPodで再試行が始まると、その実行の速度の観測は取り直します。

ステップ進捗が30秒と平均5ステップ分のうち長い方を超えて進まなければ、その速度での推定を保留します。進捗オブジェクトの取得や監視データの更新が30秒を超えて止まった場合も推定を保留します。残りステップがなく結果保存中の場合は、保存完了までの時間を推測せず `finishing and saving results` と表示します。

TUIでは画面内の経過時間と推定残り時間を1秒ごとに更新します。このためのArgo・S3への追加リクエストは行いません。新しい進捗データの取得間隔は従来どおり5秒です。

## 実験を一覧表示する

```sh
simworkflow experiment list
simworkflow experiment list --phase Running
simworkflow experiment list --limit 20
simworkflow experiment list --format json
```

設定したArgoの名前空間で、SimWorkflowの管理ラベルが付いたWorkflowだけを表示します。デフォルトは全件で、作成日時の新しい順です。同じ日時ならWorkflow名で並べます。APIの複数ページは自動で取得します。

| オプション | 動作 |
| --- | --- |
| `--phase PHASE` | `Pending`・`Running`・`Succeeded`・`Failed`・`Error` のいずれかで絞る |
| `--limit N` | 絞り込み後に最新N件を表示。0は全件 |
| `--format json` | 名前・名前空間・状態・停止要求・作成／開始／終了日時のJSON配列 |

表の `SHUTDOWN` が `Stop` の場合は停止要求済みです。停止要求後もコントローラーの処理が終わるまで `Running` のままの場合があります。日時はUTCで表示します。`--limit` は表示件数を制限するための指定で、一覧の取得範囲を制限するものではありません。

一覧はArgoのWorkflow APIから取得します。S3を検索する機能ではないため、Argoから参照できなくなった古い実験をS3だけから復元して表示することはできません。

## 実験を停止する

```sh
simworkflow experiment stop WORKFLOW_NAME
simworkflow experiment list --limit 20
```

指定したWorkflow全体にArgoの [Stop](https://argo-workflows.readthedocs.io/en/latest/cli/argo_stop/) を要求します。実行中の処理を中断し、待機している通常ステップも開始しません。一時停止や再開の機能ではありません。事前計算中に止めた実行は本実行へ進みません。

コマンドの成功は停止要求が受け付けられたことを示します。全Podが終了したことを確認してから戻る動作ではありません。`list` やArgoの画面で終了状態を確認してください。停止後は通常、Workflowの状態が `Failed` になります。`status --watch` は非成功として終了する場合があります。

対象は設定した名前空間のSimWorkflow管理下のWorkflowに限り、名前を1件ずつ完全指定します。別のツールが作成したWorkflow、`@latest` のような別名や一括セレクターは受け付けません。完了済み・停止要求済みなら追加の停止要求は送りません。

Argoの終了ハンドラーは実行可能ですが、SimWorkflowの結果収集コンテナは終了ハンドラーではありません。中断した段階の要約やログが最後までS3へ保存される保証はありません。既に保存された結果は削除せず、[ダウンロード](download.md) で取得できます。

一覧と停止にはArgoの接続設定だけが必要です。S3・レジストリの設定や認証情報、SimScopeは不要です。キーストア方式でもArgoの登録だけを参照します。停止には `workflows` の `patch` 権限が必要です。[接続トラブル](connections.md#argoの403) を参照してください。

## 失敗分の再送信と結果の統合

```sh
simworkflow experiment retry --dry-run WORKFLOW_NAME
simworkflow experiment retry WORKFLOW_NAME
simworkflow experiment results --with-retries WORKFLOW_NAME
```

`retry` は終了済みWorkflowの失敗した組み合わせを、事前計算から新しいWorkflowで実行します。`results --with-retries` は元の実験と再送信を調べ、各組み合わせの最新の成功結果を採用します。成功がなければ最新の試行を採用します。通常の `status`・`results` は指定Workflow単体の表示です。選択条件・保存情報・ダウンロードとの関係は [再送信と結果の統合](retry.md) を参照してください。

## TUIの操作

| 操作 | キー・入力 |
| --- | --- |
| 実行を選択 | ↑／↓、j／k、行のクリック |
| ページ移動 | PageUp／PageDown、Home／End |
| スクロール | マウスホイール、←／→、h／l |
| 設定詳細を開く | Enter |
| 詳細から一覧へ戻る | Esc、Backspace |
| SimScopeで開く（任意） | 詳細画面でw |
| 監視を終了 | q、Ctrl+C（一覧ではEscも利用可能） |

端末のサイズに追従し、更新時も選択とスクロール位置を保ちます。実験名と列名は画面上部に固定します。監視を閉じても実験は停止しません。Workflow完了後もTUIは最終結果を表示し、操作できます。

## 選択した実行の設定詳細

Enterで、送信時に保存した `plan.json` の内容を表示します。

- 実験・構成ID、マップの確定したバージョン・取得元・SHA-256、シード
- イメージ、シミュレーションのステップ数、種類別に適用するCPU・メモリ
- 選択したグループ名と候補名（`A=astar` など）、展開済みのモジュール構成、並列数、タイムアウト、収集対象

手元のYAMLを送信後に変更しても詳細画面には反映されません。詳細を開いている間も進捗の取得は続きます。

詳細画面の `w` でSimScopeを開けます。実行中はリアルタイム接続、終了後は保存ログを再生します。SimScopeは任意機能です。接続先を設定していない場合は設定方法を案内します。[SimScope連携](simscope.md) に接続条件をまとめています。

## 進捗の見方

`status` は1回、`status --watch` は約5秒ごとに、実行ごとの段階・状態・完了ステップ数／総ステップ数・最新スコア・進捗の更新経過時間を表示します。完了ステップ数はカーネルログから読み、経過時間からは推定しません。

各行の `ELAPSED` は、その実行の `STAGE` に表示された段階の経過時間です。`simulation` では本実行のPod開始から、`precompute` では事前計算のPod開始から計算し、初期化時間も含みます。本実行の時間に事前計算や開始前の待機時間は含めません。段階の終了後は所要時間を固定し、再試行時は新しい試行の開始から計算し直します。未開始や必要な日時を取得できない場合は `-` です。

画面上部の `Workflow Elapsed` は実験全体の経過時間で、事前計算・本実行・待機時間を含みます。TUIでは各行と全体の経過時間を毎秒更新します。通常のテキスト出力と `--plain` では状態の取得時に更新します。途中から監視してもArgoの開始日時から計算し、終了後は所要時間で固定します。

| 表示 | 意味・確認すること |
| --- | --- |
| `precompute` | 事前計算中。本実行はこの段階の成功後に開始 |
| `simulation` | 本実行の段階 |
| ステップ数が `-` | 起動待ち・初期化中、または進捗に未対応の旧ワーカー |
| `unavailable` | 進捗の取得エラー。実験そのものの失敗とは区別する |
| 更新経過時間が増える | Podの状態や通信を確認。表示は最後に保存された進捗 |

進捗対応ワーカーの結果収集コンテナは、段階ごとの `progress.json` を約5秒ごとにS3へ保存します。表示には転送・ポーリング分の遅延があります。進捗の保存失敗だけで実験を失敗にはしません。

通信エラー時は直前の表示を残して再取得します。Workflow完了後も取得エラーが残っていれば再取得し、回復後に自動更新を終了します。S3取得は最大8件並列、リクエストごとに4秒の期限です。

## テキストで監視する

```sh
simworkflow experiment status --watch --plain WORKFLOW_NAME
```

標準入力・標準出力が端末でない場合や `TERM=dumb` の場合もテキスト表示です。Workflowが完了し、進捗の取得エラーがなくなった時点で終了します。

## スコアと結果要約

`results` は実験ごとの要約をJSON配列で返します。実行状態、モジュール構成、シード、事前計算ファイル、スコア、S3の保存先を確認できます。
`stage` が要約の対象（`precompute`／`simulation`）、`phase` がその段階の結果、`workflowPhase` がWorkflow全体の状態です。
本実行の要約がまだない場合は事前計算の要約を返します。事前計算のSucceededは実験全体の完了を意味しません。
スコアは本実行の `kernel.log` に記録された最後の `Score:` または `Score=` の数値です。取得できない場合は `score` を省略し、値を推測しません。

`experiment download` で要約・ログ・設定・事前計算データを選んで取得できます。構成・段階・ログ単体での絞り込みは [ダウンロード手順](download.md) を参照してください。

## 関連ガイド

- [必要な結果をダウンロードする](download.md)
- [SimScopeの設定と接続条件](simscope.md)
- [結果の保存先と失敗・再試行の扱い](execution.md)

[ドキュメント一覧へ](README.md)
