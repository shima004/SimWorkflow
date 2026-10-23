# サービスへの接続確認

環境変数の形式だけを調べる場合:

```sh
go run ./cmd/simworkflow config check
```

実際に読み取りリクエストを送る場合:

```sh
go run ./cmd/simworkflow config check --connect
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

## Argoの403

認証成功と、Workflowへのアクセス権限は別です。APIの情報取得が成功していても、Workflow一覧が403になる場合があります。
使用するトークンのServiceAccountについて、対象名前空間にRoleとRoleBindingが必要です。

[client-rbac.yaml](../examples/kubernetes/client-rbac.yaml) は、`argo-workflows` 名前空間の `simworkflow` ServiceAccount向けの例です。
Workflowの作成・参照とPodログの参照を許可します。クラスタ管理者が対象名前空間とServiceAccountを確認したうえで適用してください。

```sh
kubectl apply -f examples/kubernetes/client-rbac.yaml
```

このファイルはCLIの利用者向けです。Workflow自身の実行に使うServiceAccountやexecutorの権限は、実行処理の実装時に別途設定します。
CLIはRBACを変更しません。

## S3の404

接続先やアクセスキーが正しくても、`S3_BUCKET` のバケットが存在しなければ成功しません。
保存先として使う既存のバケットを指定するか、管理者が新規バケットを用意します。
S3互換サービスによっては権限不足でも404を返すため、404だけで不存在と断定はできません。

## レジストリに到達しない場合

KubernetesのClusterIPは、CLIを実行する環境からルーティングできない場合があります。
クラスタ外から使うCLIには、実行環境から到達できるIngress・NodePort・LoadBalancerなどのURLを指定してください。

## 参照

- [Argo REST API](https://argo-workflows.readthedocs.io/en/latest/rest-api/)
- [Argoのセキュリティ](https://argo-workflows.readthedocs.io/en/latest/security/)
- [S3 HeadBucket](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadBucket.html)
- [Registry V2 API](https://distribution.github.io/distribution/spec/api/)
