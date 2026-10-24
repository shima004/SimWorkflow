# エージェントイメージのビルド・登録

`agent build` は、エージェントのDockerfileをビルドし、設定済みの `REGISTRY_URL` へ登録します。Java・Python共通のコマンドです。実行にはDocker CLI・Buildxと、稼働中のDockerデーモンが必要です。

## 準備

[接続設定](configuration.md) の `registry.url` または環境変数 `REGISTRY_URL` に、レジストリのルートURLを指定します。

```yaml
registry:
  url: https://registry.example.com
```

認証が必要なら `simworkflow credentials set registry` で登録します。環境変数での認証も従来と同じです。[認証情報の管理](credentials.md) を参照してください。このコマンドにArgo・S3の設定や認証情報は不要です。

## ビルドして登録する

エージェントリポジトリで実行します。Pythonのuvテンプレートの例です。

```sh
simworkflow agent build \
  --dockerfile Dockerfile.uv \
  --repository my-team/agent-python \
  --tag v1 \
  --push .
```

この例では `registry.example.com/my-team/agent-python:v1` を作ります。`--repository` はレジストリ名を含めない相対パスです。登録済みのタグを指定すると、そのタグの参照先を更新します。

成功すると、登録後のダイジェストを検証して次のように表示します。

```text
Published registry.example.com/my-team/agent-python:v1

image: registry.example.com/my-team/agent-python@sha256:...
```

表示された `image:` を実験YAMLの `spec.agents[].image` に指定します。実行ノードからも、このレジストリへのアクセスが必要です。プライベートレジストリならクラスタ側のイメージ取得用認証も別途設定します。

`--push` を省略するとローカルビルドだけを行い、認証ストアへのアクセスやレジストリへのアップロードは行いません。ベースイメージや依存パッケージの取得はDockerfileに従って行います。

```sh
simworkflow agent build --repository my-team/agent-python --tag v1 \
  --dockerfile Dockerfile.uv .
```

## オプション

| 指定 | デフォルト | 内容 |
| --- | --- | --- |
| `--repository` | 必須 | レジストリ内のリポジトリパス |
| `--tag` | `latest` | イメージのタグ |
| `--dockerfile` | `CONTEXT/Dockerfile` | Dockerfileのパス。明示時は現在のディレクトリから解決 |
| `--platform` | `linux/amd64` | 単一の `os/architecture[/variant]` |
| `--push` | `false` | ビルド後に登録する |
| 最後の引数 | `.` | ローカルのビルドコンテキスト |

オプションはコンテキストの前に指定します。複数プラットフォームの同時ビルド、リモートGitコンテキスト、追加のDockerビルド引数は現時点では扱いません。必要な場合はDockerコマンドを直接使用してください。

## 処理と認証

Dockerでビルドしてローカルに読み込み、作成されたイメージIDを指定してエクスポートします。`--push` 時はCLIがレジストリへ直接アップロードします。`docker login` やDocker認証ファイルへの書き込みは行いません。Dockerへの子プロセスにはSimWorkflowのサービス認証用環境変数を渡しません。既存のDocker側の設定やベースイメージ取得の認証はDockerが扱います。

HTTPレジストリには `REGISTRY_URL=http://...` で登録でき、Dockerデーモンのinsecure-registry設定はアップロードに不要です。HTTPSを指定した場合は証明書を検証します。認証用トークンの取得先やアップロード先が別ホストへ転送されるレジストリには対応していません。

イメージの一時アーカイブは制限した権限で作成し、コマンド終了時に削除します。ローカルのDockerイメージは残ります。イメージ相当の一時ディスク容量が必要です。`HTTP_TIMEOUT` は個々のHTTPリクエストに適用し、Dockerビルド全体には適用しません。

Dockerfileと依存関係はユーザーのものをそのまま使用します。uvの場合も既存の `pyproject.toml`・`uv.lock` を変更しません。イメージに含めるファイルはDockerfileと `.dockerignore` で管理します。

[Pythonテンプレート](../templates/agents/python/README.md)・[Javaテンプレート](../templates/agents/java/README.md)・[ドキュメント一覧](README.md)
