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

`OTEL_EXPORTER_OTLP_ENDPOINT` を設定しない場合、Telemetry は `10.254.0.10:4318` へ OTLP/HTTP で送信されます。通信には TLS を使用しません。

### 環境変数の設定例

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
export COUNTRY_ONLY="JP"
export DESCRIPTION="My Nostr Relay"
export PUBKEY="npub1..."
export CONTACT="admin@example.com"
```

## Telemetry

OpenTelemetry では、WebSocket メッセージごとにメッセージ Span を root とする 1 本のトレースを送信します。メッセージの種類に応じて `nostr.event`、`nostr.req`、`nostr.count`、`nostr.auth`、`nostr.close` の Span が作られます。parse に失敗した場合は Span 名が `ws.parse`、それ以外の種別（NOTICE など通常クライアントから送られないもの）は `ws.message` になります。メッセージ処理に由来するポリシー判定（`nostr.policy.*`）と DB 操作（`postgres.*`）の Span が同じトレースに親子関係で連なります。NIP-40 期限チェックなどの定期処理は独立したトレースになります。`nostr.req` Span は初期配信の完了（EOSE）までの時間を計測します。

HTTP リクエスト（NIP-11 など）と WebSocket ハンドシェイクには `http.request` Span を送信します（WebSocket の場合は属性 `nostr.websocket=true`）。メッセージ Span はハンドシェイクの Span へ link で関連付けられ、属性 `nostr.connection.id` で接続単位の相関が取れます。メッセージ Span には `nostr.message.type` と処理結果 `nostr.outcome`（`blocked` / `error` / `auth-required` / `invalid` / `mute` / `unsupported`。正常時は省略）が付きます。

DB 検索（`postgres.query_events`）では、Span と `db.client.operation.duration` はデータベースのスキャン完了までの時間を表し、クライアントへのイベント配信待ちは含みません。保存・集計などの操作では、Span とヒストグラムは操作完了までの時間を計測します。

Span に加えて、接続ライフサイクルと Go ランタイムの Metrics も送信します。Metrics には以下が含まれます。

- DB 操作の回数・所要時間ヒストグラム: `db.client.operation.duration`、`nostr.relay.db.operations`
- ポリシー判定回数: `nostr.relay.policy.decisions`
- 接続数（アクティブ接続のゲージ）: `nostr.relay.connections`
- 接続/切断の発生回数: `nostr.relay.connection.count`（属性 `nostr.connection.state` = `open` / `close`）
- 接続時間のヒストグラム: `nostr.relay.connection.duration`
- 接続試行の回数（現行実装はすべての接続試行を数えます）: `nostr.relay.connection.rejected`
- Go ランタイム（メモリ使用量・goroutine 数など）: `go.*`（例: `go.goroutine.count`、`go.memory.used`）

イベント ID、公開鍵、本文、タグ値、検索語、IP は Telemetry 属性に含めません。`nostr.outcome` には前述の固定ラベルのみを使用します。

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
