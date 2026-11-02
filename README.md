# khatru-redbean

Nostr relay server with internally maintained protocol, relay, and PostgreSQL implementations.

## 概要

khatru-redbean は、khatru・go-nostr・eventstore 由来の実装をリポジトリ内で保守する Nostr リレーサーバーです。PostgreSQL をバックエンドとして使用します。

## 必要な環境

- Go 1.27.3以上（CI・Docker・開発コンテナは Go 1.27.3 を使用）
- PostgreSQL データベース

PostgreSQL の保存形式と移行時の注意点は [PostgreSQL compatibility](docs/source-origins.md#postgresql-compatibility) を参照してください。

## コードの管理

- `internal/nostr/`: イベント・フィルタ・署名・JSON・必要な NIP の実装
- `internal/relay/engine/`: HTTP/WebSocket・購読・配信・認証などのリレーエンジン
- `internal/storage/postgres/`: PostgreSQL の保存・検索・削除・置換

これらのパッケージはプロジェクト内で直接保守します。汎用の暗号・WebSocket・SQL・Telemetry ライブラリは `go.mod` で管理します。

詳細は [実装の出典と保守方針](docs/source-origins.md)、[ディレクトリ構成と依存関係](docs/directory.md) を参照してください。

## ビルド

### バイナリのビルド

配布ビルドは Linux amd64/arm64 向けで、`CGO_ENABLED=0` の静的リンクバイナリを生成します。

```bash
make bin
```

バイナリは `bin/khatru-redbean`、ライセンス通知は `bin/licenses/` に生成されます。配布時は `LICENSE` と `THIRD_PARTY_NOTICES` をバイナリに同梱してください。

`arm64` 向けにビルドする場合は以下を実行してください。

```bash
make bin GOARCH=arm64
```

### Dockerイメージのビルド

```bash
make build
```

GitHub Actions では、同じタグ名で `linux/amd64` と `linux/arm64` のマルチアーキテクチャイメージを GHCR に公開します。

実行用イメージは `gcr.io/distroless/static-debian13:nonroot` を使用し、非 root ユーザーで起動します。

### その他の開発用コマンド

```bash
# テストの実行
make test

# コードフォーマットのチェック
make fmt

# 静的解析
make staticcheck

# すべてのチェック（fmt + test + staticcheck）
make check

# 開発ツールのセットアップ
make setup

# クリーンアップ
make clean
```

## 環境変数

以下の環境変数を設定してください：

| 環境変数 | 必須 | 説明 | 例 |
|---------|------|------|-----|
| `DATABASE_URL` | ✅ | PostgreSQLデータベースの接続URL | `postgres://user:password@localhost:5432/nostr?sslmode=disable` |
| `COUNTRY_ONLY` | ❌ | 特定の国からのアクセスのみを許可する（Cloudflareのカントリーコード） | `JP` |
| `DESCRIPTION` | ❌ | リレーの説明（NIP-11で表示） | `My Nostr Relay` |
| `PUBKEY` | ❌ | リレー管理者の公開鍵（NIP-11で表示） | `npub1...` |
| `CONTACT` | ❌ | リレー管理者への連絡先（NIP-11で表示） | `admin@example.com` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | ❌ | OpenTelemetry の OTLP/HTTP endpoint | `10.254.0.10:4318` |
| `OTEL_RESOURCE_ATTRIBUTES` | ❌ | trace と metric で共有する Resource 属性（カンマ区切り） | `deployment.environment.name=production` |

`OTEL_EXPORTER_OTLP_ENDPOINT` を設定しない場合、Telemetry は `10.254.0.10:4318` へ OTLP/HTTP で送信されます。通信には TLS を使用しません。

`OTEL_RESOURCE_ATTRIBUTES` は trace と metric の両方で同じ Resource として送信されます。`service.name` は常に `khatru-redbean`、`service.version` はビルド時のバージョンで明示的に設定され、環境変数で上書きされません。`service.instance.id` を指定しない場合、起動するプロセス（Provider）ごとに `crypto/rand` で 128 bit の ID を生成するため、複数プロセスの Resource が区別できます。属性値はそのまま外部へ送信されるため、トークンや接続文字列などの秘密情報は指定しないでください。ホスト名やホスト ID はアプリ側では追加しません。

### 環境変数の設定例

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
export COUNTRY_ONLY="JP"
export DESCRIPTION="My Nostr Relay"
export PUBKEY="npub1..."
export CONTACT="admin@example.com"
export OTEL_RESOURCE_ATTRIBUTES="deployment.environment.name=production"
```

## Telemetry

OpenTelemetry では、WebSocket メッセージごとにメッセージ Span を root とする 1 本のトレースを送信します。メッセージの種類に応じて `nostr.event`、`nostr.req`、`nostr.count`、`nostr.auth`、`nostr.close`、`nostr.negentropy` の Span が作られます。parse に失敗した場合は `ws.parse`、それ以外（NOTICE など、通常クライアントから送られないもの）は `ws.message` になります。

メッセージ Span は `ReadMessage` 成功直後、parse と goroutine 起動前に記録した受信時刻を開始時刻とし、応答の書き込みが完了した時刻で終了します。応答を書かない `CLOSE` と negentropy の close は、購読解除などのローカル処理が完了した時刻で終了します。クライアントへの到達やクライアント側の処理時間は含みません。メッセージ処理に由来するポリシー判定（`nostr.policy.*`）と DB 操作（`postgres.*`）の Span が同じトレースに親子関係で連なります。NIP-40 期限チェックなどの定期処理は独立したトレースになります。

投稿（`EVENT`）は 1 件につき 1 本のトレースとし、`nostr.event` を root に、ポリシー判定と DB 操作の Span を子に持ちます。保存区間を包む専用の Span や別トレースは作りません。ストレージパイプラインの完了時刻（ブロードキャストと `OK` の書き込みより前）は、メッセージ Span 上の Span Event `nostr.persistence.completed` として記録します。Span Event とメッセージ Span の `nostr.persistence.outcome` は、`stored`（新規行の保存。置換成功を含む）、`duplicate`、`superseded`、`not_stored`、`not_attempted`（validation・ポリシー拒否など）、`ephemeral`、`error`、`canceled` のいずれかです。

kind 5 の削除要求では、対象の削除に失敗すると `error` または `canceled`、著者やポリシーによる拒否は `not_attempted` になります。削除処理がエラーなく終了した後の結果は、削除要求イベント自体の保存結果です。対象行を削除した件数は、この保存ヒストグラムには数えません。

HTTP リクエスト（NIP-11 など）と WebSocket ハンドシェイクには `http.request` Span を送信します（WebSocket の場合は属性 `nostr.websocket=true`）。メッセージ Span はハンドシェイクの Span へ link で関連付けられ、属性 `nostr.connection.id` で接続単位の相関が取れます。メッセージ Span には `nostr.message.type` と処理結果 `nostr.outcome`（`ok` / `blocked` / `error` / `auth-required` / `invalid` / `mute` / `unsupported` / `write_error` / `canceled`）が付きます。未知の値は `error` に正規化され、任意のエラー文字列は属性に入りません。レスポンスの書き込みに失敗したメッセージは `ok` になりません。保存自体は成功してレスポンスだけ失敗した場合、メッセージ Span の保存結果と保存ヒストグラムには `stored` が残り、メッセージの処理結果は `write_error` になります。既に `blocked` などの結果がある場合は、書き込みに失敗してもその分類を維持します。ストレージエラーやキャンセルも `ok` として扱いません。

クライアントへの `OK true` とメッセージの `nostr.outcome=ok` は、新しい行が保存されたことを意味しません。`duplicate`、`superseded`、`not_stored`、待受のある `ephemeral` でも応答は成功になります。保存されたかどうかは `nostr.persistence.outcome=stored`、または `nostr.relay.event.persist.duration` の件数で判断してください。

DB 検索（`postgres.query_events`）では、Span と `db.client.operation.duration` はデータベースのスキャン完了までの時間を表し、クライアントへのイベント配信待ちは含みません。保存・集計などの操作では、Span とヒストグラムは操作完了までの時間を計測します。いずれもコネクションプール待ちや DB のロック待ちなど、実際の DB 呼び出しにかかった時間を含みます。`replace_event` はプロセス内の置換用 mutex 待ちも含みます。`save_event` / `replace_event` の Span には実際の書き込み結果 `nostr.persistence.outcome` が付き、`duplicate`、`superseded`、`not_stored` はエラーではありません。`nostr.relay.db.operations` と `db.client.operation.duration` は従来どおり DB 操作単位で、エラー時のみ `error.type` を持ちます。

メッセージと保存のレイテンシは、trace sampling とは独立した直接の Float64 ヒストグラムとして送信します。直接記録するため、spanmetrics の追加は不要です。両方のヒストグラムに同じメッセージ Span のコンテキストを渡し、既定の設定では sampled な trace/span ID が exemplar としてバケットあたり最大 1 件付くことがあります。全サンプルに付くわけではありません。このプロセスは trace を常に sample しますが、exemplar の保存・表示には Collector と backend 側の対応も必要です。

- `nostr.relay.message.duration`（unit `s`）: 受信時刻から処理完了（応答の書き込み完了、または `CLOSE` / negentropy close のローカル処理完了）までのサーバー側レイテンシ。1 メッセージにつき 1 回だけ記録します。explicit boundaries は `0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10` 秒です。属性は `nostr.message.type` と `nostr.outcome`。`EVENT` では `nostr.event.class` と、パイプライン完了後の `nostr.persistence.outcome` も付きます。フィルタ形状やイベント ID などの高カーディナリティ属性はコピーしません。
- `nostr.relay.event.persist.duration`（unit `s`）: 受信時刻からストレージパイプライン完了までのサーバー側レイテンシ。同じ boundaries を使い、`nostr.persistence.outcome=stored` のサンプルだけを記録します。属性は `nostr.event.class` と `nostr.persistence.outcome=stored`。`duplicate`、`superseded`、`not_stored`、`not_attempted`、`ephemeral`、`error`、`canceled` はヒストグラムに含めず、メッセージ Span の `nostr.persistence.completed` Event と `nostr.relay.message.duration` の属性で確認します。このヒストグラムの件数は、実際に行が保存されたイベントの件数であり、保存されなかったイベントは含みません。

`nostr.message.type` の値は `event` / `req` / `count` / `auth` / `close` / `negentropy` / `parse` / `message` です。`nostr.event.class` の値は `regular` / `replaceable` / `addressable` / `ephemeral` / `deletion` です。

これらのレイテンシはサーバーが受信してからレスポンスを書き終えるまで、または保存処理が完了するまでの時間で、クライアントとサーバー間のネットワーク RTT やクライアント側の処理時間は含みません。`REQ` の通常経路は初期結果と `EOSE` の書き込みまでです。拒否された `REQ` は `EOSE` を待たず、拒否応答の書き込みで終了します。検索や集計に失敗した `REQ` / `COUNT` は `nostr.outcome=error` になり、`ok` にはなりません。ライブ購読の継続時間や、最初のイベントだけの時間は計測しません。

Span に加えて、接続ライフサイクルと Go ランタイムの Metrics も送信します。Metrics には以下が含まれます。

- メッセージ処理時間ヒストグラム: `nostr.relay.message.duration`（unit `s`）
- イベント保存時間ヒストグラム（保存成功のみ）: `nostr.relay.event.persist.duration`（unit `s`）
- DB 操作の回数・所要時間ヒストグラム: `db.client.operation.duration`、`nostr.relay.db.operations`
- ポリシー判定回数: `nostr.relay.policy.decisions`
- 接続数（アクティブ接続のゲージ）: `nostr.relay.connections`
- 接続/切断の発生回数: `nostr.relay.connection.count`（属性 `nostr.connection.state` = `open` / `close`）
- 接続時間のヒストグラム: `nostr.relay.connection.duration`
- 接続試行の回数（現行実装はすべての接続試行を数えます）: `nostr.relay.connection.rejected`
- Go ランタイム（メモリ使用量・goroutine 数など）: `go.*`（例: `go.goroutine.count`、`go.memory.used`）

イベント ID、公開鍵、本文、タグ値、検索語、サブスクリプション ID、IP は新しい Metric 属性に含めません。`nostr.outcome` と `nostr.persistence.outcome` には前述の固定ラベルのみを使用します。メッセージ Span には従来どおり接続 ID とフィルタ形状のバケット属性が付きますが、これは Span のみで、レイテンシ Metric にはコピーしません。

このリポジトリが提供するのはアプリケーション側の Span・Metric・Resource 属性です。OTel Collector でのラベル変換、ダッシュボード、アラート、exemplar の可視化設定はインフラ側の設定であり、この変更には含まれません。Collector や backend で未設定の場合、ここで説明した属性や exemplar はそのままでは表示されない場合があります。

## 使い方

### リレー監視

```bash
./bin/khatru-redbean monitor --relay wss://relay.example
```

監視は起動直後に確認し、その後1時間ごとに確認します。最新イベントの最大許容経過時間は12時間、各接続のタイムアウトは30秒、接続・タイムアウト・プロトコルエラーは30秒間隔で追加2回試行します。空・古いイベント・拒否応答は再試行しません。異常のリマインド間隔は12時間です。異常、原因変更、復旧はログに記録されます（通知処理のタイムアウトは5秒）。SIGINT/SIGTERMで終了します。

`--interval`、`--max-age`、`--timeout`、`--retries`、`--retry-interval`、`--remind-interval` で変更できます。イベントの署名・ID・作成日時を確認し、`EOSE` による取得完了まで待ちます。鮮度はリレーの受信時刻ではなくイベントの `created_at` を基準にします。投稿がない場合とDB障害は区別できず、書き込みだけの障害は直接検知しません。監視コマンドにはDB設定は不要で、Telemetry送信も行いません。

### サーバーの起動

```bash
./bin/khatru-redbean serve [オプション]
```

### コマンドラインオプション

| オプション | 短縮形 | デフォルト | 説明 |
|-----------|-------|-----------|------|
| `--port` | `-p` | `9999` | リッスンポート番号 |

## 実行例

### デフォルトポート（9999）で起動

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
./bin/khatru-redbean serve
```

### カスタムポートで起動

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
./bin/khatru-redbean serve --port 8080
```

### 日本からのアクセスのみ許可

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
export COUNTRY_ONLY="JP"
./bin/khatru-redbean serve
```

## 機能

- ✅ PostgreSQLバックエンドによるイベントの永続化
- ✅ NIP-11（リレー情報ドキュメント）対応
- ✅ 国別アクセス制限（Cloudflare経由の場合）
- ✅ グレースフルシャットダウン
- ✅ インデックス対象のタグ値の長さ制限（最大100バイト）
- ✅ NIP-01, 11, 40, 42, 45, 70, 86 対応

## シャットダウン

サーバーは `Ctrl+C` または `SIGTERM` シグナルでグレースフルシャットダウンします。

PostgreSQL へのアクセスが失敗した場合もグレースフルシャットダウンを行い、終了コード 1 でプロセスを終了します。コンテナや systemd の再起動ポリシーを設定しておくことで、DB 接続断から自動復旧できます。クライアントによるリクエストのキャンセル、重複イベント、フィルタの件数超過など、DB 接続障害ではないエラーでは終了しません。
