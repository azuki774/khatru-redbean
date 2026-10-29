# ディレクトリ構成

```text
.
├── cmd/                          # serve / monitor の CLI
├── internal/
│   ├── config/                   # 設定と NIP-11 情報
│   ├── logger/                   # ログ設定
│   ├── monitor/                  # リレーの実接続監視
│   ├── nostr/                    # 共通型・署名・JSON・クライアント処理
│   │   ├── nip11/                # 必要な NIP 実装（他に40/42/45/70/77/86）
│   │   └── libsecp256k1/         # 任意の cgo 実装とライセンス
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

`nostr` は共通の型とプロトコル実装です。`storage` は `nostr` を使用し、`engine` は `nostr` と storage の共通契約・共有エラーなどを使用します。`storage/postgres` は engine に依存せず、engine も PostgreSQL の具体実装に依存しません。`engine/policies` は engine を使用しますが、逆向きの依存はありません。既存の `relay` がこれらを組み立てます。

Nostr 関連実装は通常の所有パッケージであり、vendor や旧モジュールを参照するラッパーではありません。取り込み範囲と元ソースについては [source-origins.md](source-origins.md) を参照してください。
