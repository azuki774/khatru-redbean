# 取り込んだ Nostr 関連実装

このプロジェクトは、以下の固定版の使用パッケージと必要な推移依存パッケージを、通常の `internal/` パッケージとして取り込んでいます。`vendor/`、別 Go モジュール、旧 import を残す `replace` や互換エイリアスは使用していません。これら3モジュールへの外部依存はありません。暗号、WebSocket、SQL ドライバ、Telemetry などの汎用ライブラリは `go.mod` で管理します。

## 取得元と配置

取得にはローカル Go モジュールキャッシュの配布ソースを使用しました。khatru の upstream リポジトリに対する Git 操作やネットワークアクセスは行っていません。

| 元のモジュール | バージョン | 元コミット | 配置先 |
| --- | --- | --- | --- |
| `github.com/nbd-wtf/go-nostr` | `v0.52.3` | `78da15566351c20b57eff50907844607a06d9a10` | `internal/nostr/` |
| `github.com/fiatjaf/khatru` | `v0.19.1` | `8d5fc324f7a0bb2af083d8566b531ca3357ee652` | `internal/relay/engine/` |
| `github.com/fiatjaf/eventstore` | `v0.17.5` | `4983a415892bea9ff4d2f60e1900651ebedb98ee` | `internal/storage/` |

取り込み元モジュールのチェックサム:

```text
github.com/nbd-wtf/go-nostr v0.52.3 h1:Xd87pXfJEJRXHpM+fLjQQln8dBNNaoPA10V7BbyP4KI=
github.com/fiatjaf/khatru v0.19.1 h1:n2m+cL9pdeb8WMhIDYbjct7jCirS9eHuMR0R7i2JGjw=
github.com/fiatjaf/eventstore v0.17.5 h1:/3CRthtZmkcTM01IxEiebydM5MKYZhZcQBKMSbLuWCs=
```

### go-nostr 由来

ルートパッケージと、`nip11`、`nip40`、`nip42`、`nip45`、`nip70`、`nip77`、`nip86` および必要な HyperLogLog・negentropy サブパッケージを取り込んでいます。同じパッケージ内にあるクライアント処理も維持しています。未使用の SDK、他の NIP パッケージ、サンプルアプリは対象外です。

生成済み Event/Filter JSON コードと、`sonic`、`debug`、`js`、`libsecp256k1` のビルド条件別ソースを保持しています。任意の cgo 実装に必要な `libsecp256k1/` の C ソース・生成済みテーブルも保持しています。通常の配布ビルドは従来どおり `CGO_ENABLED=0` の Go 実装を使用します。`sonic` や cgo を有効にしたビルドは、通常ビルドと別に検証してください。

### khatru 由来

ルートのリレー実装と `policies` パッケージを取り込んでいます。HTTP/WebSocket、購読と配信、認証、削除、期限切れ、管理 API、negentropy の既存処理を含みます。未使用の Blossom サーバー、サンプル、upstream のドキュメントサイトは取り込んでいません。

起動・設定・独自ポリシー・Telemetry の組み立ては `internal/relay`、汎用リレー処理は `internal/relay/engine` が担当します。engine から PostgreSQL の具体実装や policies を import しません。従来のデフォルトメタデータ・ログ文字列は維持し、このサービスの NIP-11 情報は既存の設定処理で上書きします。

### eventstore 由来

ルートのストレージ契約・共有エラー・補助処理、PostgreSQL バックエンド、必要な internal ヘルパーを取り込んでいます。他の DB バックエンドは含みません。

PostgreSQL のテーブル、インデックス、`tags_to_tagvalues` 関数と検索・保存 SQL は v0.17.5 の形式を維持しています。[v0.17.6 以降のタグ保存形式変更](https://github.com/fiatjaf/eventstore/commit/359b43d6010b4060808de4720524db5756370b91)は取り込んでいません。この取り込みのための DB 移行・イベント再投入は不要です。将来スキーマやタグの格納形式を変更する場合は、既存イベントとインデックスの移行を別途設計してください。

## ローカルでの変更

- import とパッケージ名を所有パッケージの配置に合わせて変更しています。
- 内部エラー名を `ErrUnknownLabel`、`ErrTooManyIDs` などに統一しました。元のエラー文字列と重複エラーの共有は維持しています。旧名の互換エイリアスはありません。
- context キーをパッケージ固有の型にしました。取得側と設定側は同じキーを共有し、アプリのテストも非公開キーの数値を再現せず、実接続の context を使用します。
- 静的解析に従い、未使用の定数・代入・無効な break・冗長な return/ループと未使用レシーバー名を除去しました。JSON のフィールド・署名対象・SQL の意味は変更していません。
- subscription ID の作業バッファはポインターをプールし、Put 時のスライスのボックス化を避けています。
- 決定的なプロトコル・リスナー・negentropy のテストを移しました。外部サービスに依存するテスト、未使用バックエンドが必要なテスト、SQL ソース文字列だけを固定するテストは持ち込んでいません。

後継の `fiatjaf.com/nostr` API への移行や、新機能・ストレージ切り替えは行っていません。

## ライセンスと配布

- go-nostr: MIT、Copyright (c) 2022 nbd。`internal/nostr/LICENSE.md` を保持します。
- khatru と eventstore: Unlicense。それぞれの配置先に元の `LICENSE` を保持します。
- go-nostr に同梱された libsecp256k1: 個別の MIT 著作権表示を保持します。元の配布キャッシュで欠けていた `COPYING` は [libsecp256k1 の許諾文](https://github.com/bitcoin-core/secp256k1/blob/master/COPYING)から補完しています。

[THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES) に、取り込んだソースの許諾文と同梱 C ソースの著作権表示をまとめています。プロジェクトの `LICENSE` とともに、ソース・バイナリ・コンテナの配布物へ同梱してください。`make bin` は出力先の `licenses/` に、Docker イメージは `/usr/share/licenses/khatru-redbean/` に両ファイルを配置します。

外部依存の個別ライセンスも引き続き適用されます。この文書と notices は、外部依存全体のライセンス監査の代わりではありません。

## 今後の保守

取り込んだコードは自分たちの実装として直接修正します。元ライブラリの `go get` で更新する仕組みはありません。変更時は `make check` と amd64/arm64 ビルドに加え、PostgreSQL 上で既存イベント・タグ検索・投稿・購読・重複・置換・削除・認証・監視・DB 障害時の終了を確認してください。データ形式の変更は、通常のコード更新とは分けて扱います。
