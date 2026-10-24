# 認証情報の管理

接続先は `config.yaml`、認証情報はOSの資格情報ストアへ保存します。新しく `config init` した設定はこの方式です。CLIに平文ファイルへの自動保存機能はありません。

## 登録する

先に [接続設定](configuration.md) のURL、Argoの名前空間、S3のバケット名を設定してください。

```sh
simworkflow credentials set s3

# 認証が必要な場合だけ登録
simworkflow credentials set argo
simworkflow credentials set registry
# レジストリでBearerトークンを使う場合
simworkflow credentials set registry --auth token
```

値は非表示の対話入力で受け付けます。コマンド引数や標準入力のパイプでは受け付けません。S3はアクセスキーID・シークレットキー・任意のセッショントークン、Argoはトークン、レジストリはユーザー名とパスワード、またはトークンを登録します。

認証情報はサービスごとに一組で保存します。入力途中のキャンセルや入力の検証エラーでは保存内容を変更しません。同じ接続先への再登録は、それまでの一組を置き換えます。レジストリの認証方式を変えたときも古いパスワードやトークンを組み合わせません。

共通設定は次の方式を選んでいる必要があります。

```yaml
credentials:
  store: keyring
```

## 確認・削除する

```sh
simworkflow credentials status
simworkflow credentials status s3
simworkflow credentials delete s3
simworkflow config check
simworkflow config check --connect
```

`status` は設定中の接続先について、OSストアへの登録状態だけを表示します。値を表示するコマンドはありません。環境変数の指定状況や実サービスでの有効性は表示しないため、設定全体は `config check`、読み取りアクセスは `--connect` で確認します。

削除は現在の接続先の登録だけを対象にします。既に未登録でも成功します。Argo・レジストリが未登録なら匿名アクセスになります。S3の未登録は設定エラーです。キーストアが利用できない場合や壊れた登録データは、未登録とは区別してエラーになります。

## 接続先ごとの分離

登録は以下の設定に紐づきます。

| サービス | 登録を区別する設定 |
| --- | --- |
| Argo | サーバーURL・名前空間 |
| S3 | エンドポイントURL・バケット名 |
| Registry | レジストリURL |

別の接続先へ変更すると古い認証情報は使用しません。必要な接続先へ切り替えて登録・削除してください。`--config FILE` で別環境の設定を選ぶ場合も同じ規則です。OSストアの管理用識別子はURLなどから作るハッシュで、認証情報は含めません。

## OSごとの要件

| OS | 保存先・要件 |
| --- | --- |
| macOS | Keychain。OSの `/usr/bin/security` を使用 |
| Windows | Windows Credential Manager |
| Linux | D-BusのSecret Serviceと利用可能なloginコレクション（GNOME Keyringなど） |

実装には [go-keyring](https://github.com/zalando/go-keyring/tree/v0.2.8) を使用します。CGOは不要ですが、OS側の資格情報ストアは必要です。ロックされている場合はOSの操作で解除してください。OSストアの解除やアクセス許可を求められる場合があります。

WSLではLinuxのストアを使用し、WindowsのCredential Managerへ自動接続しません。Secret Serviceを用意するか、Windows版CLIを使うか、以下の環境変数方式を選んでください。利用できないOSストアの代わりに、平文ファイルへ自動で保存・読み込みすることはありません。

保存するJSON一式は2400バイトまでです。これはWindowsの保存上限やmacOSのコマンド入力上限に収まるための制限です。大きな一時トークンなどで超える場合は環境変数方式を使用してください。

## 環境変数とCI

キーストア方式でも、認証用の環境変数が1つでも指定されたサービスは、環境変数だけで認証します。S3ならキーIDとシークレットキー、一時認証ならセッショントークンも一緒に指定します。OSストアの値を補完して組み合わせることはしません。明示的な空文字も上書きとして扱います。

CIやヘッドレス環境でOSストアへアクセスさせない場合は、`CREDENTIALS_STORE=environment` を設定します。必要な認証情報はCIのSecrets管理機能などからプロセス環境へ渡してください。

```yaml
# GitHub Actionsのジョブ／ステップ内の設定例
env:
  CREDENTIALS_STORE: environment
  ARGO_WORKFLOW_TOKEN: ${{ secrets.ARGO_WORKFLOW_TOKEN }}
  S3_ACCESS_KEY_ID: ${{ secrets.S3_ACCESS_KEY_ID }}
  S3_SECRET_ACCESS_KEY: ${{ secrets.S3_SECRET_ACCESS_KEY }}
```

接続先・名前空間・バケット名なども別途設定します。この例だけでは実験は実行できません。`environment` 方式は既存の `.env`・YAMLからの認証情報読み込みも許可する互換モードです。環境変数だけを使うなら `--config='' --env-file=''` でファイル読み込みも無効にできます。

## 既存の.env・YAMLから移行する

1. 共通YAMLで `credentials.store: keyring` を指定し、接続先などは維持します。
2. `credentials set` の非表示入力で必要な認証情報を登録します。ファイルからの自動コピーは行いません。
3. `credentials status` と `config check --connect` で確認します。認証用の環境変数を設定済みなら、それらがOSストアより優先される点に注意してください。
4. 確認後、`.env`・YAMLに残った認証情報を自分で削除します。CLIは既存ファイルを変更しません。

`.env` に `CREDENTIALS_STORE=environment` がある場合はYAMLより優先されるため、その指定も見直します。キーストア方式ではファイル内の認証情報を無視しますが、ファイル自体は削除しません。従来の設定で `credentials.store` を省略した場合は、互換性のため `environment` 方式を継続します。

[ドキュメント一覧へ](README.md)
