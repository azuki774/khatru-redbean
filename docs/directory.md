# ディレクトリ構成案
.
├── bin/                   # バイナリ出力先
├── cmd/
│   ├── root.go            # CLIのルート定義
│   └── serve.go           # 'serve' コマンドの実装 (entrypoint)
├── docs/
│   └── directory.md       # このファイル
├── internal/
│   ├── config/            # 環境変数・設定読み込み
│   │   └── config.go
│   ├── logger/            # ロガー設定
│   │   └── logger.go
│   └── relay/             # Khatruインスタンスの初期化・組み立て工場
│       └── instance.go    # NewRelay() はここに書く
├── .devcontainer/         # Dev Container設定
├── .github/
│   └── workflows/         # GitHub Actionsワークフロー
├── Dockerfile             # Dockerイメージ定義
├── LICENSE                # ライセンス
├── Makefile               # ビルド・実行用Makefile
├── README.md              # プロジェクト説明
├── go.mod                 # Goモジュール定義
├── go.sum                 # Go依存関係チェックサム
└── main.go                # cmd.Execute() を呼ぶだけ
