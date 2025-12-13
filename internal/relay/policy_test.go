package relay

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey int

const (
	// wsKey matches the khatru internal context key for WebSocket connections.
	// khatru uses iota (which equals 0) as the key type.
	wsKey contextKey = 0
)

// createMockContext は、指定されたCF-IPCountryヘッダーを持つモックコンテキストを作成します。
func createMockContext(country string) context.Context {
	req := httptest.NewRequest("GET", "/", nil)
	if country != "" {
		req.Header.Set("CF-IPCountry", country)
	}

	ws := &khatru.WebSocket{
		Request: req,
	}
	//lint:ignore SA1029 khatruは内部でint型のキーを使用しているため、int(wsKey)でキャストする
	return context.WithValue(context.Background(), int(wsKey), ws)
}

func Test_instance_RestrictCountry(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for receiver constructor.
		port        int
		databaseUrl string
		countryOnly string
		info        *nip11.RelayInformationDocument
		// Named input parameters for target function.
		ctx   context.Context
		event *nostr.Event
		want  bool
		want2 string
	}{
		{
			name:        "CountryOnly未設定の場合は常に許可",
			port:        3334,
			databaseUrl: "",
			countryOnly: "", // 空文字列の場合は国制限なし
			info:        &nip11.RelayInformationDocument{},
			ctx:         context.Background(),
			event:       &nostr.Event{},
			want:        false, // reject = false (許可)
			want2:       "",    // メッセージなし
		},
		{
			name:        "コンテキストに接続情報がない場合はブロック",
			port:        3334,
			databaseUrl: "",
			countryOnly: "JP", // 日本のみに制限
			info:        &nip11.RelayInformationDocument{},
			ctx:         context.Background(),
			event:       &nostr.Event{},
			want:        true,                                            // reject = true (ブロック)
			want2:       "blocked: unable to verify country information", // エラーメッセージ
		},
		{
			name:        "許可されていない国（US）からのアクセスはブロック",
			port:        3334,
			databaseUrl: "",
			countryOnly: "JP", // 日本のみに制限
			info:        &nip11.RelayInformationDocument{},
			ctx:         createMockContext("US"), // CF-IPCountry: US
			event:       &nostr.Event{},
			want:        true, // reject = true (ブロック)
			want2:       "blocked: only access from JP is allowed",
		},
		{
			name:        "許可された国（JP）からのアクセスは許可",
			port:        3334,
			databaseUrl: "",
			countryOnly: "JP", // 日本のみに制限
			info:        &nip11.RelayInformationDocument{},
			ctx:         createMockContext("JP"), // CF-IPCountry: JP
			event:       &nostr.Event{},
			want:        false, // reject = false (許可)
			want2:       "",
		},
		{
			name:        "CF-IPCountryヘッダーが空の場合はブロック",
			port:        3334,
			databaseUrl: "",
			countryOnly: "JP", // 日本のみに制限
			info:        &nip11.RelayInformationDocument{},
			ctx:         createMockContext(""), // CF-IPCountryヘッダーなし
			event:       &nostr.Event{},
			want:        true, // reject = true (ブロック)
			want2:       "blocked: only access from JP is allowed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := NewInstance(tt.port, tt.databaseUrl, tt.countryOnly, tt.info)
			got, got2 := i.RestrictCountry(tt.ctx, tt.event)
			if got != tt.want {
				t.Errorf("RestrictCountry() reject = %v, want %v", got, tt.want)
			}
			if got2 != tt.want2 {
				t.Errorf("RestrictCountry() msg = %v, want %v", got2, tt.want2)
			}
		})
	}
}
