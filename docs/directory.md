# ディレクトリ構成

```text
.
├── cmd/                          # serve / monitor の CLI
├── internal/
│   ├── config/                   # 設定と NIP-11 情報
│   ├── logger/                   # ログ設定
│   ├── monitor/                  # リレーの実接続監視
│   ├── nostr/                    # 共通型・署名・JSON・クライアント処理
│   ├── relay/                    # 起動・独自ポリシー・Telemetry の組み立て
│   │   └── engine/               # 汎用リレーエンジン
│   │       └── policies/         # 汎用の投稿・検索ポリシー
│   ├── storage/                  # 保存契約・共有エラー・補助処理
│   │   ├── internal/             # ストレージ実装専用ヘルパー
│   │   └── postgres/             # PostgreSQL の保存・検索・削除・置換
│   └── telemetry/                # OpenTelemetry の初期化
├── docs/
│   ├── directory.md
│   └── source-origins.md         # 取り込み元・版・変更方針・ライセンス
├── bin/                          # ビルド出力（licenses/ を含む）
├── .github/workflows/            # CI とコンテナ公開
├── .devcontainer/
├── Dockerfile
├── LICENSE
├── THIRD_PARTY_NOTICES
├── Makefile
├── go.mod
├── go.sum
└── main.go
```

## 依存関係

- `internal/nostr` は、共通のイベント型と Nostr プロトコル処理を提供します。
- `internal/storage` は `nostr` を使用し、保存の共通契約・エラーを定義します。`storage/postgres` が PostgreSQL の具体実装を担当します。
- `internal/relay/engine` は `nostr` と `storage` の共通契約を使用します。PostgreSQL の具体実装と `engine/policies` には依存しません。
- `internal/relay/engine/policies` は `engine` を使用するポリシー集です。
- `internal/relay` が、エンジン・PostgreSQL・設定・独自ポリシー・Telemetry を組み立てます。

元ライブラリのバージョン、実装範囲、互換性の制約は [Source provenance](source-origins.md) を参照してください。
