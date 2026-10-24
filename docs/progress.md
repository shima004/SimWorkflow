# 進捗・スコアの確認

`WORKFLOW_NAME` は `experiment submit` が返した名前です。オプションはWorkflow名より前に置きます。

```sh
simworkflow experiment list
simworkflow experiment status WORKFLOW_NAME
simworkflow experiment status --watch WORKFLOW_NAME
simworkflow experiment status --watch --plain WORKFLOW_NAME
simworkflow experiment results WORKFLOW_NAME
```

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
