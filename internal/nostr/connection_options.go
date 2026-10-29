//go:build !js

package nostr

import (
	"crypto/tls"
	"net/http"
	"net/textproto"

	ws "github.com/coder/websocket"
)

var defaultConnectionOptions = &ws.DialOptions{
	CompressionMode: ws.CompressionContextTakeover,
	HTTPHeader: http.Header{
		textproto.CanonicalMIMEHeaderKey("User-Agent"): {"github.com/azuki774/khatru-redbean/internal/nostr"},
	},
}

func getConnectionOptions(requestHeader http.Header, tlsConfig *tls.Config) *ws.DialOptions {
	if requestHeader == nil && tlsConfig == nil {
		return defaultConnectionOptions
	}

	return &ws.DialOptions{
		HTTPHeader:      requestHeader,
		CompressionMode: ws.CompressionContextTakeover,
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		},
	}
}
