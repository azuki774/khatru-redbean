# khatru-redbean

Nostr relay server based in [khatru](https://github.com/fiatjaf/khatru)

## 概要

khatru-redbeanは、[khatru](https://github.com/fiatjaf/khatru)をベースにしたNostrリレーサーバーです。PostgreSQLをバックエンドとして使用し、NIP-11に対応しています。

## 必要な環境

- Go 1.21以上
- PostgreSQL データベース

## ビルド

### バイナリのビルド

```bash
make bin
```

バイナリは `bin/khatru-redbean` に生成されます。

### Dockerイメージのビルド

```bash
make build
```

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

### 環境変数の設定例

```bash
export DATABASE_URL="postgres://user:password@localhost:5432/nostr?sslmode=disable"
export COUNTRY_ONLY="JP"
export DESCRIPTION="My Nostr Relay"
export PUBKEY="npub1..."
export CONTACT="admin@example.com"
```

## 使い方

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
- ✅ 大きなタグの防止（最大100タグ）
- ✅ NIP-01, 11, 40, 42, 70, 86 対応

## シャットダウン

サーバーは `Ctrl+C` または `SIGTERM` シグナルでグレースフルシャットダウンします。
