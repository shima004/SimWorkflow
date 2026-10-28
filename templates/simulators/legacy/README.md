# 既存イメージのサーバーを利用する補助手順

ユーザーが改造したシミュレーターをSimWorkflowで使うための全体の手順は、[改造したシミュレーターを使う](../README.md) を参照してください。ビルド済み配布物から作る標準のDockerfileも、1つ上のディレクトリにあります。

このディレクトリは、サーバー本体が既存のローカルイメージ内にある場合や、固定パスへ出力するスコアログの互換対応が必要な場合の補助テンプレートです。`legacy` というディレクトリ名でも、利用対象を特定年度・特定ユーザーのイメージには限定していません。

## サーバー本体を用意する

Python 3.11以降とDockerを使い、SimWorkflowリポジトリのルートで実行します。イメージ名とサーバー配置先は、自分のイメージに合わせて変更します。出力先には存在しないディレクトリを指定します。

```sh
python3 templates/simulators/legacy/prepare-context.py \
  --source-image registry.example.com/my-team/modified-server:v1 \
  --source-path /opt/rcrs-server \
  --output artifacts/my-simulator-build
```

元イメージはローカルに必要です。抽出時に元コンテナは起動しません。出力先には `rcrs-server/`、`java-wrapper.sh`、元イメージのdigestとJAR・ライブラリのSHA-256を記録した `source.json` が作成されます。

取り出した本体を通常のログ保存方式で使う場合は、[標準のビルド手順](../README.md#3-ビルドして登録する) で `templates/simulators/Dockerfile` を使用します。

## 固定パスのスコアログを引き継ぐ場合

このディレクトリの `Dockerfile` と `java-wrapper.sh` は、スコア関数が `$RCRS_HOME/logs/log/scores.log` に書き込む実装に対応しています。ユーザーの実装がこの方式を使う場合は、次の形でビルド・登録できます。

```sh
simworkflow agent build \
  --repository my-team/simulator \
  --tag v1 \
  --dockerfile templates/simulators/legacy/Dockerfile \
  --push \
  artifacts/my-simulator-build
```

サーバーのJAR・ライブラリ・ソース・LICENSEは変更せずに収録し、現在のCLIに対応するランチャーを追加します。Javaランタイムはランチャー用ベースイメージを使用します。元イメージ情報は `/usr/share/simworkflow/legacy-source.json` にも収録します。

起動ラッパーはスコア用の `RCRS_HOME` を一時ディレクトリへ設定し、`logs/log` から `--log` で指定された段階別ログへリンクします。ランチャーのサーバー配置は `--root /opt/rcrs-server` で指定します。結果バンドルが走査するログ領域にはリンクを置きません。

`outputs.collect` に `simulatorLogs` を含めると、`scores.log` も結果バンドルに収録されます。内容の書式・計算式はユーザーのスコア関数のものを維持します。別の固定パスを使う実装では、その実装に合わせてラッパーを変更してください。

登録後のイメージ指定や動作確認は、[全体ガイド](../README.md#4-実験yamlから指定する) に従います。取り出したサーバーや個人用検証ログは `artifacts/` 配下に置き、Gitへ登録しません。
