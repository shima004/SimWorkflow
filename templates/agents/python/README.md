# Pythonエージェントのコンテナテンプレート

ADF互換のPythonエージェント用です。uvテンプレートは [rescue-python](https://github.com/shima004/rescue-python) の実行確認済み構成を基準にしています。pipテンプレートは従来の [adf-sample-agent-python](https://github.com/adf-python/adf-sample-agent-python) 向けです。
uvテンプレートは、実行確認済みのPython 3.14・ADF Core v0.2.14に統一しています。pipテンプレートは従来のPython 3.13向けです。
このテンプレート自体はエージェントのソースコードを含みません。Java連携を使わないPythonエージェントを対象とします。

| 方式 | 対象 | Dockerfile |
| --- | --- | --- |
| [uv](#uvを使う場合) | Python 3.14・rescue-python基準 | `Dockerfile.uv` |
| [pip](#pipを使う場合) | Python 3.13・従来サンプル向け | `Dockerfile` |

## uvを使う場合

`pyproject.toml` と `uv.lock` で依存関係を管理する例です。
共通の `Dockerfile.uv` を使用します。Python 3.14とuv 0.13.5が入った公式イメージ `ghcr.io/astral-sh/uv:0.13.5-python3.14-trixie-slim` で `uv sync --locked` を実行し、作成した仮想環境を `python:3.14-slim-trixie` の実行用イメージへ配置します。ビルド側と実行側でPython・Debianのバージョンをそろえています。
使用できるイメージとタグは [公式のDocker連携手順](https://docs.astral.sh/uv/guides/integration/docker/) を参照してください。

1. `Dockerfile.uv` と `.dockerignore` をエージェントリポジトリのルートへコピーします。
2. 新規プロジェクトでは `pyproject.uv.toml` を `pyproject.toml` という名前でコピーします。既存の `pyproject.toml`・`uv.lock` がある場合はそれらを使用し、上書きしません。Python 3.14に対応していることを確認し、独自依存も保持してください。
3. 比較したいモジュールを `src` 以下に追加します。
4. 以下のコマンドでロックファイルを生成し、イメージをビルド・登録します。

```sh
uv lock --python 3.14
docker build --platform linux/amd64 -f Dockerfile.uv \
  -t registry.example.com/my-team/agent-python:v1 .
docker login registry.example.com
docker push registry.example.com/my-team/agent-python:v1
```

`uv lock` は依存関係とPython 3.14の取得にネットワークを使う場合があります。
生成した `uv.lock` と `pyproject.toml` はエージェントリポジトリで管理してください。既存のロックをそのまま使う場合は `uv lock --check --python 3.14` で整合性だけを確認できます。
テンプレートにはユーザーの依存構成に応じて作成する `uv.lock` を同梱していません。
ロックファイルが存在しない場合や `pyproject.toml` と整合しない場合はDockerビルドが失敗します。

テンプレートの `pyproject.uv.toml` は、確認済みのADF Core v0.2.14と `rcrscore` v0.2.3をコミットで固定しています。
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
起動時にはuvによる依存の解決・同期を行いません。設定ファイルの配置先と、以下の `docker run` の使い方はpipテンプレートと共通です。
ホストの `.venv` は同梱の `.dockerignore` で除外し、イメージ内で作成した環境を使います。

詳細は [uvの依存管理](https://docs.astral.sh/uv/concepts/projects/dependencies/) を参照してください。

uvテンプレートの標準レイアウトは次のとおりです。追加のデータディレクトリが必要な場合は `Dockerfile.uv` の実行ステージに `COPY` を追加してください。起動時に使う `python` は `/app/.venv/bin/python` です。

```text
agent/
├── Dockerfile.uv
├── .dockerignore
├── pyproject.toml
├── uv.lock
├── main.py
├── config/
│   ├── launcher.yaml
│   └── module.yaml
└── src/
```

出典ラベルを設定したい場合はビルド時に `--build-arg AGENT_SOURCE=https://github.com/OWNER/AGENT --build-arg AGENT_REVISION=COMMIT` を追加します。
[rescue-pythonの検証例](../../../examples/validation/rescue-python-vc/README.md) もこの `Dockerfile.uv` を使用します。

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

## コンテナの取り決め

| 項目 | パス・内容 |
| --- | --- |
| 作業ディレクトリ | `/app` |
| 元のモジュール設定 | `/app/config/module.yaml` |
| 起動設定 | `/app/config/launcher.yaml` |
| 起動処理 | `python main.py` |
| ワーカーが生成する実験用設定 | `/work/modules/<role>/module.yaml` |
| シミュレータ接続先の引数 | `--host simulator --port 27931` |

ワーカーは `/app/config/module.yaml` を元に種類別の設定を生成し、`launcher.yaml` の `adf.agent.moduleconfig.filename` を生成ファイルへ変更します。元設定はこの固定パスに配置してください。
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
現行のuvテンプレートは `rescue-python` の既存ロックと、新規プロジェクト用ひな形から生成したロックの両方でDockerビルド・ヘルプ起動・A*／D* Lite／クラスタリングのインポートを確認しています。ロックが古い場合のビルド拒否も確認済みです。
Argo上の事前計算・本実行・S3保存の検証は従来の実験イメージの記録で、今回の統一後にArgo実験は再実行していません。pipテンプレートのDockerビルドも未確認です。[検証記録](../../../docs/validation.md)を参照してください。
