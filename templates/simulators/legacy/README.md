# 旧RCRSサーバーの互換イメージ

ローカルイメージの `rcrs-server` を取り出し、現在のSimWorkflowが使うランチャーで起動できるイメージを作成します。旧サーバーのJAR・ライブラリ・ソース・LICENSEは変更せずに収録します。ランチャーはDockerfileでdigestを固定しています。

## ビルド

リポジトリのルートで実行します。Python 3.11以降とDockerが必要です。出力先は存在しないディレクトリを指定します。

```sh
python3 templates/simulators/legacy/prepare-context.py \
  --source-image tob213/rrs-sim:latest \
  --output artifacts/legacy-server-build

docker build --platform linux/amd64 \
  -f templates/simulators/legacy/Dockerfile \
  -t registry.example.com/simworkflow/rcrs-server-legacy:v1 \
  artifacts/legacy-server-build
```

抽出時に元コンテナは起動しません。`source.json` に元イメージのdigestとJAR・ライブラリのSHA-256を保存し、作成イメージの `/usr/share/simworkflow/legacy-source.json` にも収録します。元イメージはローカルに必要です。取り出したサーバーや検証ログは `artifacts/` 配下に置き、Gitへ登録しません。

実行ノードから取得できるレジストリへ登録した後、実験YAMLの `spec.simulator.image` をその参照へ変更します。helperイメージを変更する必要はありません。マップの `score.function` は元の独自クラスを指定できます。

## 起動契約

- サーバー配置：`/opt/rcrs-server`
- 起動：`java -jar /opt/rcrs-server/jars/rcrs-server-launcher.jar precompute|comprun --nogui --map DIR --config DIR --log DIR --team NAME`
- 実行ユーザー：`rcrs`、Javaランタイムはランチャー用ベースイメージを使用
- `ScoreCalcFunction` が固定パスへ出力する `scores.log` は、`--log` で指定した段階別ディレクトリへ保存

Javaの起動ラッパーは旧クラスを変更せず、スコア出力用の `RCRS_HOME` とランチャーの明示的な `--root` を設定します。スコア用のリンクは一時ディレクトリへ作成し、結果バンドルが走査するログ領域にはリンクを置きません。

`outputs.collect` に `simulatorLogs` を含めると、`scores.log` も結果バンドルに収録されます。各行は元の実装どおりであり、厳密なJSONではありません。計算対象がない場合の `NaN` も変更しません。

## 確認した元イメージ

`tob213/rrs-sim@sha256:ce32722975f6b3c296b2a4d54a43c966fb24e951fa3bc65320e4b8508a7a0974`

このイメージの `standard.jar` には `ScoreCalcFunction` と追加指標の実装があります。別の元イメージを使う場合は、必要なスコアクラスとランチャーとの互換性を確認してください。
