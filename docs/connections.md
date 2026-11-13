# 接続・実行のトラブル対応

まず、設定の問題か、サービスへの接続・権限の問題かを切り分けます。接続確認に成功しても実験が動かない場合は、[実行や結果取得で困ったとき](#実行や結果取得で困ったとき) を参照してください。

## 設定と読み取り接続を確認する

[接続設定](configuration.md) を用意した後、設定の不足や形式を調べる場合（キーストア方式ではOSストアを参照）：

```sh
simworkflow config check
```

実際に読み取りリクエストを送る場合:

```sh
simworkflow config check --connect
```

`--connect` は3サービスを並行して調べ、サービスごとの結果を表示します。1つでも失敗すれば終了コードは1です。
認証情報、接続先の値、レスポンス本文は出力しません。通信エラーも値を含まないメッセージに置き換えます。
TLS証明書は検証し、リダイレクトは追跡しません。`HTTP_TIMEOUT` が各HTTPリクエストの制限時間です。

| 対象 | 操作 | 確認する権限・範囲 |
| --- | --- | --- |
| Argo Workflow | `GET /api/v1/workflows/{namespace}?listOptions.limit=1` | 指定名前空間のWorkflow一覧の読み取り |
| S3 | 署名付き `HeadBucket` | `S3_BUCKET` の存在とアクセス |
| Registry | `GET /v2/` | Registry V2 APIへのアクセス |

書き込み、バケットの作成、Workflowの起動、イメージの登録は行いません。
S3の `HeadBucket` 成功だけではオブジェクトの読み書き権限は確認できません。
レジストリのAPIルートへのアクセス成功だけでは、個別リポジトリのpull/push権限は確認できません。

レジストリの認証は匿名・Basic・設定済みBearerトークンに対応します。
Bearerチャレンジには匿名または同じオリジンのトークン取得先で対応します。Basicの認証情報は別オリジンのトークン取得先へ送信しません。
設定済みBearerトークンが拒否された場合は、そのまま認証エラーにします。

## 設定・認証で困ったとき

| 症状 | 確認・対処 |
| --- | --- |
| `config init` が既存ファイルのエラーになる | `config path` で場所を調べ、既存の設定を編集する |
| YAMLを変えても接続先が変わらない | 環境変数・作業ディレクトリの `.env` がYAMLより優先される。[優先順位](configuration.md#読み込み方式と優先順位) を確認する |
| OSストアが使えない・ロックされている | OS側でストアを解除する。Linux／WSLではSecret Serviceが必要。[OSごとの要件](credentials.md#osごとの要件) を確認する |
| 環境変数でS3のキーIDだけ変えると認証できない | OSストアのシークレットキーとは組み合わない。認証情報をサービス単位で一組指定する |
| 接続先変更後に認証エラーになる | 認証情報は接続先ごとに別登録。新しい設定で `credentials set` を実行する |

## Argoの403

認証成功と、Workflowへのアクセス権限は別です。APIの情報取得が成功していても、Workflow一覧が403になる場合があります。
`argoWorkflow.namespace` が正しいことを確認し、管理者にトークンの対象と権限を確認してください。ServiceAccountのトークンを使う場合は、対象名前空間にRoleとRoleBindingが必要です。

[client-rbac.yaml](../examples/kubernetes/client-rbac.yaml) は、`argo-workflows` 名前空間の `simworkflow` ServiceAccount向けの例です。
Workflowの作成・参照・停止に必要なpatchと、Podログの参照を許可します。クラスタ管理者が対象名前空間とServiceAccountを確認したうえで適用してください。

```sh
# 管理者が、このリポジトリのルートで実行する例
kubectl apply -f examples/kubernetes/client-rbac.yaml
```

`experiment stop` はArgoのStop APIを使用し、内部で `workflows` の `patch` が必要です。一覧・送信に成功しても停止だけ403になる場合は、この権限を確認してください。以前のRBACサンプルを適用した環境では、管理者が更新後の内容を確認・適用する必要があります。CLIコマンド自体はRBACを変更しません。

このファイルはCLIの利用者向けです。Workflow自身の実行に使うServiceAccountとexecutor権限には [executor-rbac.yaml](../examples/kubernetes/executor-rbac.yaml) を使用します。
CLIはRBACを変更しません。

## S3の404

接続先やアクセスキーが正しくても、`S3_BUCKET` のバケットが存在しなければ成功しません。
保存先として使う既存のバケットを指定するか、管理者が新規バケットを用意します。
S3互換サービスによっては権限不足でも404を返すため、404だけで不存在と断定はできません。

## レジストリに到達しない場合

KubernetesのClusterIPは、CLIを実行する環境からルーティングできない場合があります。
クラスタ外から使うCLIには、実行環境から到達できるIngress・NodePort・LoadBalancerなどのURLを指定してください。

CLIが接続できても、実行ノードからイメージを取得できるとは限りません。CLIの `credentials set registry` はノード側のpull用認証を設定しません。プライベートレジストリでは、管理者が実行用ServiceAccountなどへイメージ取得用認証を設定します。

## 実行や結果取得で困ったとき

```sh
simworkflow experiment list --limit 20
simworkflow experiment status WORKFLOW_NAME
# 保存済みの要約がある場合
simworkflow experiment results WORKFLOW_NAME
```

| 症状 | 確認・対処 |
| --- | --- |
| 送信コマンドが途中で終了し、送信できたか不明 | `experiment list` で新しいWorkflowの有無を確認する。同じYAMLの再送信は別の実験を作る |
| `Pending` が続く | Argoの画面・Podのイベントで、CPU・メモリ不足や配置条件を確認する。エージェントの `resources` は3種類それぞれに適用される |
| イメージ取得で失敗する | Podのイベントでイメージ名・到達性・ノード側の認証を確認する。`ImagePullBackOff` などはCLIの接続確認だけでは検出できない |
| `OOMKilled` で終了する | 対象コンテナを特定し、次の実験YAMLで該当するメモリを見直す。JVMヒープの上限とコンテナのメモリは別の設定 |
| `unavailable` が続く | CLIからS3への接続・読み取り権限とPodの状態を確認する。表示の取得失敗は実験の失敗とは限らない |
| ステップが終わっても `Running` のまま | `ACTIVITY` が結果の圧縮・アップロード中なら完了を待つ。詳細は [進捗の説明](progress.md#シミュレーション終了後の処理) を参照する |
| 事前計算が成功したのに実験が完了しない | `stage` が `simulation` へ進んだかを確認する。`precompute` の成功は全体の成功ではない |
| `results` が要約未取得のエラーになる | 両段階の要約がまだない実行が含まれる可能性がある。実行中は `status` を使い、失敗時はArgo・Podのログを確認する |
| ログやスコアが取得できない | 送信時の `outputs.collect`、対象段階、結果保存の成否を確認する。保存していないログは後から収集できない |
| `download` が既存の出力先への追加を拒否する | 取得元やローカルファイルが変わっていないか確認する。取り直す場合は新しい `--output` を指定する |
| 署名付きURLが期限切れになる | 終了済みWorkflowの対象を [再送信](retry.md) する。未完了分も含める場合は `--include-unfinished` を使う |
| 定義サイズ・設定容量・所要時間の上限で送信できない | 実験の分割、並列数やタイムアウトの見直しを行う。[制限](execution.md) を確認する |

完了Podは削除待ちになってから10分後に自動削除されます。S3へ保存されなかったログや終了理由を調べる場合は、削除前に管理者へ確認してください。問い合わせ時にはCLIのバージョン、Workflow名、run ID、エラーメッセージを伝えると調査しやすくなります。認証情報や署名付きURLを含む生成YAMLは共有しないでください。

## 参照

- [Argo REST API](https://argo-workflows.readthedocs.io/en/latest/rest-api/)
- [Argoのセキュリティ](https://argo-workflows.readthedocs.io/en/latest/security/)
- [S3 HeadBucket](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadBucket.html)
- [Registry V2 API](https://distribution.github.io/distribution/spec/api/)

[ドキュメント一覧へ](README.md)
