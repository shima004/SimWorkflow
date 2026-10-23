# Pythonエージェントのコンテナテンプレート

[adf-sample-agent-python](https://github.com/adf-python/adf-sample-agent-python) をベースにしたエージェント用です。
現在の [ADF Coreの依存設定](https://github.com/adf-python/adf-core-python/blob/main/pyproject.toml) に合わせ、Python 3.13を使用します。
このテンプレート自体はエージェントのソースコードを含みません。Java連携を使わないPythonエージェントを対象とします。

## pipを使う場合

1. このディレクトリの `Dockerfile` と `.dockerignore` を、エージェントリポジトリの `main.py` があるディレクトリへコピーします。
2. 比較したいモジュールのPythonソースを `src` 以下に追加します。必要な依存ライブラリを `requirements.txt` に追加します。
3. 同じディレクトリで次のコマンドを実行します。

```sh
docker build -t registry.example.com/my-team/agent-python:v1 .
docker login registry.example.com
docker push registry.example.com/my-team/agent-python:v1
```

イメージ名は利用するレジストリ・チーム名に変更してください。
ビルド時にGitリポジトリとPythonパッケージを取得するため、ネットワーク接続が必要です。

ADF Coreの `uv.sources` で指定されている `rcrscore` は、pip用に先にGitからインストールします。
テンプレートの `RCRS_CORE_REF` は `v0.2.3` です。使用するADF Coreの依存設定に合わせて変更できます。

```sh
docker build --build-arg RCRS_CORE_REF=v0.2.3 \
  -t registry.example.com/my-team/agent-python:v1 .
```

元のサンプルの `requirements.txt` はADF CoreのGitリポジトリを参照します。
実験を再現する際には、その参照をコミットに固定し、その他の依存バージョンとイメージのダイジェストも固定してください。

## uvを使う場合

`pyproject.toml` と `uv.lock` で依存関係を管理する例です。
Python 3.13とuvが入った公式イメージ `ghcr.io/astral-sh/uv:0.13.5-python3.13-trixie-slim` をベースに使い、`uv sync --locked` で依存をインストールします。
使用できるイメージとタグは [公式のDocker連携手順](https://docs.astral.sh/uv/guides/integration/docker/) を参照してください。

1. `Dockerfile.uv` と `.dockerignore` をエージェントリポジトリのルートへコピーします。
2. `pyproject.uv.toml` を同じ場所に `pyproject.toml` という名前でコピーします。既に `pyproject.toml` がある場合は、必要な依存とGitソース設定を既存ファイルに統合します。
3. 比較したいモジュールを `src` 以下に追加します。
4. 以下のコマンドでロックファイルを生成し、イメージをビルド・登録します。

```sh
uv lock --python 3.13
docker build -f Dockerfile.uv \
  -t registry.example.com/my-team/agent-python:v1 .
docker login registry.example.com
docker push registry.example.com/my-team/agent-python:v1
```

`uv lock` は依存関係とPython 3.13の取得にネットワークを使う場合があります。
生成した `uv.lock` と `pyproject.toml` はエージェントリポジトリで管理してください。
テンプレートにはユーザーの依存構成に応じて作成する `uv.lock` を同梱していません。
ロックファイルが存在しない場合や `pyproject.toml` と整合しない場合はDockerビルドが失敗します。

テンプレートの `pyproject.uv.toml` は、確認済みのADF Coreコミットと `rcrscore` のGitソースを指定しています。
Gitソースはエージェント側の `[tool.uv.sources]` に明示します。依存パッケージ自身のGitソース設定に頼らない形です。
独自の追加依存も `pyproject.toml` で管理します。このDockerfileでは `requirements.txt` はインストールに使いません。

```sh
# 既存のrequirements.txtの依存を取り込む場合
uv add -r requirements.txt

# 新たな依存を追加する場合（numpyは例）
uv add numpy
```

取り込み後は `[tool.uv.sources]` の参照先を確認してください。ADF Coreを変更した場合は、そのバージョンに対応する `rcrscore` も指定します。
ローカルでの実行例は以下です。シミュレータは別途起動しておきます。

```sh
uv run --locked python main.py --host localhost --port 27931
```

Docker内ではビルド時に `/app/.venv` へインストールし、起動時は `/app/.venv/bin/python main.py` を直接実行します。
起動時にはuvによる依存の解決・同期を行いません。設定ファイルの配置先と、以下の `docker run` の使い方はpip版と共通です。
ホストの `.venv` は同梱の `.dockerignore` で除外し、イメージ内で作成した環境を使います。

詳細は [uvの依存管理](https://docs.astral.sh/uv/concepts/projects/dependencies/) を参照してください。

## コンテナの取り決め

| 項目 | パス・内容 |
| --- | --- |
| 作業ディレクトリ | `/app` |
| 元のモジュール設定 | `/app/config/module.yaml` |
| 起動設定 | `/app/config/launcher.yaml` |
| 起動処理 | `python main.py` |
| 実験用設定の配置先 | `/app/config/module.yaml` |
| シミュレータ接続先の引数 | `--host simulator --port 27931` |

サンプルの `launcher.yaml` の `adf.agent.moduleconfig.filename` が `config/module.yaml` であることを前提にします。
その参照先をカスタマイズした場合は、実験用設定の配置先も合わせて変更します。
モジュール設定の実装パスは、コンテナ内でインポートできる必要があります。
`matrix` の候補をすべてイメージに含めれば、組み合わせごとの再ビルドは不要です。コードを変更した際は新しいイメージを作ります。

## 手動で設定ファイルを渡す例

シミュレータが、Dockerネットワーク `rescue-network` 内の `simulator:27931` で起動している想定です。
手元の `module.yaml` を元設定からコピーし、比較したいモジュールの値を変更してから実行します。

```sh
cp config/module.yaml module.yaml
# module.yaml を編集
docker run --rm \
  --network rescue-network \
  --mount "type=bind,src=$PWD/module.yaml,dst=/app/config/module.yaml,readonly" \
  registry.example.com/my-team/agent-python:v1 \
  --host simulator --port 27931
```

設定ファイル1個をマウントすることで、`launcher.yaml` など他の設定ファイルを引き続き利用できます。
エージェント数などは元の `launcher.yaml`、または `-a`・`-f`・`-p` などの起動引数で指定します。
Javaモジュールの利用にはJVMやゲートウェイなどの追加設定が必要で、このテンプレートには含めていません。

プラットフォームは元設定へ選択したモジュールを反映し、`launcher.yaml` の参照先を実験専用の生成ファイルへ変更します。事前計算データも実験専用領域に保持し、本実行へ引き継ぎます。[実行手順](../../../docs/execution.md) を参照してください。
uv版は公開サンプルで依存解決・Dockerビルド・ヘルプ起動に成功し、生成した設定をADFの `ModuleConfig` で読み込んで基準実装をインポートできることを確認しています。
uv版ではArgo上の事前計算、データを読み込む本実行、結果のS3保存も確認しています。pip版のDockerビルドは未確認です。[検証記録](../../../docs/validation.md)を参照してください。
