# マップの登録と利用

マップは `map/` と `config/` をまとめ、S3のカタログへ名前・バージョンを付けて登録します。変更時には新しいバージョンを作ります。シミュレーターのイメージへマップを組み込む必要はありません。

## 登録

```text
my-map/
  map/
    map.gml
    scenario.xml
  config/
    kernel.cfg
    common.cfg
    その他の設定ファイル
```

```sh
simworkflow map register \
  --name kobe --version v1 --path ./my-map \
  --description '比較実験用の神戸マップ' --origin '配布元のURL・生成条件など'

# ZIPのルートにmap/とconfig/がある既存ZIPも登録可能
simworkflow map register \
  --name kobe --version v2 --path ./kobe-v2.zip

simworkflow map list --format text
simworkflow map inspect --name kobe --version v1
```

ディレクトリの場合、`map/` と `config/` だけをZIPへ収録します。これらの中にはシミュレーターへ渡すファイルだけを置いてください。同じファイル内容からは同じZIPを生成します。ZIP入力は元のバイト列を保存します。

登録時に必須ファイル、XML構文、エージェント数、危険なパスを検査し、ZIPのSHA-256・サイズ・種類別個体数・登録日時・説明を記録します。シミュレーターとの完全な互換性は実行して確認してください。ZIPと展開結果の上限はそれぞれ2GiB、最大10,000エントリです。

名前は小文字英数字とハイフンの1～63文字、バージョンは小文字英数字・ドット・アンダースコア・ハイフンの1～63文字で、いずれも先頭・末尾は英数字です。`latest` は参照用の予約語です。

## 実験YAML

```yaml
spec:
  simulator:
    image: ghcr.io/shima004/rcrs-server:v1.0.4
    resources:
      cpu: 2
      memory: 8Gi
    parameters:
      timesteps: 300
  conditions:
    maps:
      - name: kobe
        version: v1
      - name: kobe-newest
        ref: kobe
        version: latest
    seeds: [1, 2]
```

`name` は実験内の識別名です。カタログ名が異なる場合だけ `ref` を指定します。`latest` は登録日時が最も新しいバージョンを選び、同時刻ならバージョン名の辞書順で最後を選びます。バージョン名の数値順やSemVer順ではありません。

`render` と `submit` はカタログを読み、具体的なバージョン・S3参照先・SHA-256を実行計画へ保存します。以後の新規登録で送信済み実験のバージョンは変わりません。ワーカーは取得したZIPのハッシュを照合し、一致しなければ起動を中止します。

`validate` と `plan` はオフラインで動作するため、カタログの存在確認・バージョンの解決は行いません。登録マップを含むオフライン計画の実行IDは暫定です。実行時の計画は解決後のバージョンとハッシュからIDを生成します。

既存の直接参照も利用できます。

```yaml
maps:
  - name: existing-map
    source: s3://rescue-maps/maps/kobe.zip
    # sha256: ZIPのSHA-256（64桁の小文字16進数、省略可能）
```

`source` と `version` は同時指定できません。直接参照で再現性が必要な場合は `sha256` も指定します。

`parameters.timesteps` は1～2,147,483,647の整数です。展開した設定の `kernel.timesteps` を事前計算・本実行の両方で上書きします。省略時はマップに含まれる設定を使用します。シードと同様に元のZIPを変更しないため、ステップ数を変えるたびにマップを登録し直す必要はありません。

## 保存と同時登録の条件

```text
maps/<name>/<version>/map.zip
maps/<name>/<version>/metadata.json
```

`S3_BUCKET` の上記プレフィックスを使用します。メタデータの公開が完了したバージョンだけが一覧へ現れます。既存のバージョンは更新できません。アップロード途中で失敗した場合、同じZIPなら同じバージョンの登録を再試行できます。

同じバージョンへの並行登録の排他制御にはS3の条件付きPUT（`If-None-Match: *`）を使用します。**保存先がこの条件を並行処理でも正しく実装することが前提です。** CLI以外からの直接変更を防ぐバケット権限管理も別途必要です。

2026年10月24日の検証環境（利用者申告: `rustfs/rustfs:1.0.0-alpha.83`）では、異なる内容を同じ新規キーへ並行PUTすると両方成功する症状を確認しています。この環境で同じ名前・バージョンの登録を並行実行すると、上書きを防げません。解消・再検証までは登録処理を一元化して逐次実行してください。ハッシュの照合は実行時の改変検出であり、登録時の排他制御の代用にはなりません。

RustFSの関連する[公式Issue #1659](https://github.com/rustfs/rustfs/issues/1659)は修正・確認済みとして閉じられていますが、今回の症状との原因の一致や、更新による解消は未確認です。

[ドキュメント一覧へ](README.md)
