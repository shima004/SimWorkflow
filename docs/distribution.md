# 別環境への配布

システム側の配布対象はhelperイメージです。シミュレーター・エージェントの起動管理、S3からの設定取得、事前計算データの引き継ぎ、進捗・結果保存とCLIを含みます。

配布先は `ghcr.io/shima004/simworkflow-worker` です。初回公開の状態と固定digestは末尾の公開記録を確認してください。サポートする実行Podのアーキテクチャは `linux/amd64` です。

## イメージの利用

公開済みのタグまたはdigestを取得し、実験を送信します。

```sh
docker pull ghcr.io/shima004/simworkflow-worker:latest

go run ./cmd/simworkflow experiment submit \
  -f experiment.yaml \
  --config-storage s3 \
  --helper-image ghcr.io/shima004/simworkflow-worker:latest
```

`latest` はmainの更新で変わります。再現性が必要な実験では `ghcr.io/shima004/simworkflow-worker@sha256:...` または `sha-<40桁のコミットSHA>` を指定してください。helperを公開しても、Argo・S3の接続設定や認証情報は利用環境ごとに設定します。

## GoやソースコードなしでCLIを利用する

Linux amd64／WSLでは、イメージから静的リンク済みのCLIを取り出せます。

```sh
worker_container_id=$(docker create --platform linux/amd64 ghcr.io/shima004/simworkflow-worker:latest)
docker cp "$worker_container_id:/usr/local/bin/simworkflow" ./simworkflow
docker rm "$worker_container_id"
chmod +x ./simworkflow

./simworkflow experiment validate -f experiment.yaml
```

このCLIにも `.env` 自動読み込み、TUI、SimScope起動機能があります。ブラウザ起動には利用環境の既定ブラウザ設定が必要です。

CLIをコンテナのまま使うこともできます。作業ディレクトリをマウントして設定・実験YAML・ダウンロード結果を共有します。

```sh
docker run --rm -it \
  -v "$PWD:/workspace" -w /workspace \
  ghcr.io/shima004/simworkflow-worker:latest \
  experiment validate -f experiment.yaml
```

コンテナ内の `localhost` はホストと異なります。Argo・S3に到達できるURLを設定してください。コンテナ版はホストのブラウザを起動しないため、TUIの `w` を使う場合は取り出したCLIを使用します。

## 実験用イメージ・マップとの関係

helper以外に、実験YAMLで指定するシミュレーターとエージェントのイメージが必要です。シミュレーターは既存の `ghcr.io/shima004/rcrs-server:v1.0.4` を使用できます。独自エージェントは利用者がアクセス可能なレジストリへ登録します。マップは利用環境のS3へ登録し、Argoの名前空間とServiceAccountを準備してください。

ローカルレジストリの `172.16.201.150:5000/...` を含む既存の検証YAMLは、この環境での記録です。別環境では到達可能なエージェントイメージへ置き換えます。実験データ・認証情報をhelperへ含める必要はありません。

## GitHub Actionsで継続公開する

[helper-image.yml](../.github/workflows/helper-image.yml) は次の処理を行います。

- Pull request：Goのraceテスト・vet・依存整合性検査
- main更新：上記検査後、GHCRへ `latest` と `sha-<コミットSHA>` を公開
- `v*` タグ：上記検査後、同名タグと `sha-<コミットSHA>` を公開
- 手動実行：指定したrefのコミット固定タグを公開（mainならlatestも更新）

公開にはActionsの `GITHUB_TOKEN` とジョブ単位の `packages: write` を使用します。個人用のS3キーやArgoトークンをActionsへ設定する必要はありません。Dockerfileは `cmd/` と `internal/` だけをコピーし、公開イメージへ `.env`・マップ・取得済みログを取り込みません。

GHCRでは新しいパッケージの初期公開範囲はprivateです。初回登録後、パッケージ設定の **Change visibility → Public** を設定し、認証なしでpullできることを確認してください。リポジトリをPublicにする操作とは別です。既存の実験定義のイメージ参照は自動変更しません。

参考：[GitHub公式のGHCR公開手順](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Container registryの公開範囲](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

## 初回公開記録（2026年10月24日）

GHCRへの登録と `latest`・`sha-58fd58333b221537b282ad160d34ee0407d227a8` の作成を確認しています。

```text
ghcr.io/shima004/simworkflow-worker@sha256:2e204474aa5c348c0d2196cc83adde038170fd9885bb09c2e83b4a4fd31cc027
```

ビルド・Goの全体テスト・actionlint・コンテナ内でのCLI起動を確認済みです。現時点では匿名取得が拒否されており、[パッケージ設定](https://github.com/users/shima004/packages/container/simworkflow-worker/settings)でPublicへの切り替えが必要です。切り替え後に認証なしでの取得を確認します。

GitHub Actionsの設定はローカルで作成済みですが、現在の接続からリポジトリへのアクセス権がないため、GitHubへの反映は未完了です。リポジトリへアクセス可能な環境から今回のコミットをpushすると、その後のmain更新・タグ作成で公開処理が動きます。

詳細は [検証記録](../examples/validation/public-helper-20261024.json) を参照してください。
