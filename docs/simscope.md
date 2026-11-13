# SimScopeで実行を見る（任意）

SimScopeを用意すると、本実行のリアルタイム表示と保存ログの再生ができます。CLIの進捗画面から、対象の実行を選んで開きます。

```sh
simworkflow experiment status --watch WORKFLOW_NAME
```

上下キーで実行を選び、Enterで設定詳細を開き、`w` を押します。CLIを実行しているマシンの既定ブラウザでSimScopeを開きます。TUIはそのまま操作でき、Escで一覧へ戻れます。`w` を連打しても準備処理は重複しません。

接続先は共通設定ファイルの `simScope.url`、環境変数または `.env` の `SIMSCOPE_URL` で指定します。デフォルトの接続先はありません。未設定でも実験の実行や監視は利用でき、`w` を押した際に設定方法を表示します。

```yaml
simScope:
  url: https://simscope.example.com
```

環境変数または `.env` で指定する場合：

```dotenv
SIMSCOPE_URL=https://simscope.example.com
```

SimScopeの [URLパラメータ](https://github.com/shima004/SimScope#url-パラメータ) に合わせ、エージェント名を `team`、マップ名とバージョンを `map` に渡します。

## 実行中：リアルタイム接続

本実行のPodがRunningなら、現在のPod IPを `host`、カーネルのポート `27931` を `port` に渡します。SimScopeサーバーがWebSocketからカーネルへTCP接続します。SimScopeサーバーから対象PodのTCP 27931へ到達できるネットワーク構成が必要です。接続直後からの観察となり、Perceptionなどの詳細機能には保存ログの再生を使用します。

独自・旧helperを使う場合は、現在のPod IPをArgoまたはS3の進捗情報から取得できる必要があります。対応状況が不明な場合は、CLIの既定helperで新しく送信して確認してください。送信済み実験のhelperはCLIの更新だけでは変わりません。

事前計算中・起動待ち・進捗がまだない場合は、画面に理由を表示します。本実行の起動後に再度 `w` を押してください。ブラウザ操作で実験を再実行することはありません。

## 終了後：保存ログの再生

本実行がSucceeded・Failed・Errorなら、保存済みの `rescue.log.7z` のURLを `url` に渡して自動読み込みします。失敗した実行でも、その段階の要約とログが保存されていれば再生できます。

初回はCLIが結果バンドルから `logs/simulation/rescue.log.7z` を取り出し、S3の閲覧用領域へ保存します。新しい `bundle.zip` では必要なファイルを部分取得し、旧 `bundle.tar.gz` ではバンドル全体を取得して展開します。一時ディスク容量が必要です。

2回目以降は同じ結果の閲覧用ログを再利用します。本実行の再試行で結果が変わった場合は別の保存先を使います。ローカルの一時ファイルは終了時に削除し、閲覧用のS3オブジェクトは残します。

SimScopeには当該ログだけを取得できる有効期限7日間の署名付きURLを渡します。URLや認証情報はTUI・ダウンロードのマニフェストに出力せず、バケットを公開する設定変更も行いません。S3の読み取りと閲覧用領域への書き込み権限が必要です。期限後は再度 `w` を押して新しいURLで開けます。

`outputs.collect` に `simulatorLogs` を含めてください。ログ・要約の転送がまだ終わっていない場合は、保存完了後に再度 `w` を押します。SimScopeサーバーがS3へ到達でき、URL読み込み用の `/fetch-proxy` が有効になっている必要があります。

## ブラウザの起動

Linuxは `xdg-open`、macOSは `open`、Windowsは `rundll32 url.dll,FileProtocolHandler` を使用します。Linuxで `xdg-open` がない場合は、`wslview`、`rundll32.exe`（WSLのWindows既定ブラウザ）、`gio open`、`sensible-browser` の順に探します。GUIセッションと既定ブラウザを設定してください。起動に失敗した場合はTUIにメッセージを表示し、一覧・詳細の操作を継続できます。`--plain` の表示ではキー操作は使用できません。

[ドキュメント一覧へ](README.md)
