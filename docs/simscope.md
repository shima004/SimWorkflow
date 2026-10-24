# TUIからSimScopeを開く

```sh
go run ./cmd/simworkflow experiment status --watch WORKFLOW_NAME
```

上下キーで実行を選び、Enterで設定詳細を開き、`w` を押します。CLIを実行しているマシンの既定ブラウザでSimScopeを開きます。TUIはそのまま操作でき、Escで一覧へ戻れます。`w` を連打しても準備処理は重複しません。

接続先は環境変数または `.env` の `SIMSCOPE_URL` で指定します。省略時は `http://172.16.201.173` です。

```dotenv
SIMSCOPE_URL=http://172.16.201.173
```

SimScopeの [URLパラメータ](https://github.com/shima004/SimScope#url-パラメータ) に合わせ、エージェント名を `team`、マップ名と版を `map` に渡します。

## 実行中：リアルタイム接続

本実行のPodがRunningなら、現在のPod IPを `host`、カーネルのポート `27931` を `port` に渡します。SimScopeサーバーがWebSocketからカーネルへTCP接続します。SimScopeサーバーから対象PodのTCP 27931へ到達できるネットワーク構成が必要です。接続直後からの観察となり、Perceptionなどの詳細機能には保存ログの再生を使用します。

Argo APIがPod IPを返さない場合はS3の本実行の `progress.json` を使用します。CLIが生成するWorkflowは、Kubernetes Downward APIの `status.podIP` を収集コンテナの `SIMWORKFLOW_POD_IP` に渡します。対応ワーカーはそのIPを進捗に保存します。CLIは実行ID・run ID・段階・開始／更新時刻を照合し、再試行前のPodへ接続しません。

リアルタイム対応ワーカー：

```text
172.16.201.150:5000/simworkflow/worker:simscope-20261024
sha256:20e2206d0a44413e2f3e5ea2427054380cad4f233ee5a3c2fcfb7e2632d253aa
```

更新後のCLIで、このワーカーを `experiment submit --helper-image` に指定して新規送信します。古いワーカーの実行を監視してもIPは追加されません。事前計算中・起動待ち・進捗がまだない場合は画面に理由を表示するので、本実行の起動後に再度 `w` を押してください。既存の実験は自動で再実行しません。

## 終了後：保存ログの再生

本実行がSucceeded・Failed・Errorなら、保存済みの `rescue.log.7z` のURLを `url` に渡して自動読み込みします。失敗した実行でも、その段階の要約とログが保存されていれば再生できます。

初回はCLIが本実行の `bundle.tar.gz` を取得し、`logs/simulation/rescue.log.7z` だけを取り出して、S3の次の領域へ保存します。

```text
experiments/<execution-id>/<run-id>/viewer/<summary-finishedAt-UnixNano>/rescue.log.7z
```

一時ファイルは処理終了時に削除し、閲覧用のS3オブジェクトは残します。2回目以降は同じ要約の保存済みオブジェクトを再利用します。再試行後の要約は別の保存先になります。初回準備にはバンドル取得・展開・ログ転送分の時間と一時ディスク容量が必要です。

SimScopeには当該ログだけを取得できる有効期限24時間の署名付きURLを渡します。URLや認証情報はTUI・ダウンロードmanifestに出力せず、バケットを公開する設定変更も行いません。S3の読み取りと閲覧用領域への書き込み権限が必要です。期限後は再度 `w` を押して新しいURLで開けます。

`outputs.collect` に `simulatorLogs` を含めてください。ログ・要約の転送がまだ終わっていない場合は、保存完了後に再度 `w` を押します。SimScopeサーバーがS3へ到達でき、URL読み込み用の `/fetch-proxy` が有効になっている必要があります。

## ブラウザの起動

Linuxは `xdg-open`、macOSは `open`、Windowsは `rundll32 url.dll,FileProtocolHandler` を使用します。Linuxで `xdg-open` がない場合は、`wslview`、`rundll32.exe`（WSLのWindows既定ブラウザ）、`gio open`、`sensible-browser` の順に探します。GUIセッションと既定ブラウザを設定してください。URLはシェルを介さず引数として渡します。起動に失敗した場合はTUIにメッセージを表示し、一覧・詳細の操作を継続できます。`--plain` の表示ではキー操作は使用できません。
