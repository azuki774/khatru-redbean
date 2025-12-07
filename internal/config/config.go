package config

import "github.com/nbd-wtf/go-nostr/nip11"

// khatru-redbean 向けのコンフィグを設定
func NewNIP11InfoForredbean() *nip11.RelayInformationDocument {
	var nip11 nip11.RelayInformationDocument
	nip11.Name = "redbean"
	nip11.Description = "khatru server customized by redbean"
	// PubKey        string `json:"pubkey"`
	nip11.Contact = "npub1t3hk2zz6xuq7g3gljtf8jtzt0a967f6rchmnkd39vt4egjxelgmqngqav8"
	nip11.SupportedNIPs = []any{1, 11, 40, 42, 70, 86}
	nip11.Software = "https://github.com/azuki774/khatru-redbean"
	// Version       string `json:"version"`
	// Limitation     *RelayLimitationDocument  `json:"limitation,omitempty"`
	nip11.RelayCountries = []string{"JP"}
	nip11.LanguageTags = []string{"ja"}
	// Tags           []string                  `json:"tags,omitempty"`
	// PostingPolicy  string                    `json:"posting_policy,omitempty"`
	// PaymentsURL    string                    `json:"payments_url,omitempty"`
	// Fees           *RelayFeesDocument        `json:"fees,omitempty"`
	// Retention      []*RelayRetentionDocument `json:"retention,omitempty"`
	// Icon           string                    `json:"icon"`
	// Banner         string                    `json:"banner"`

	return &nip11
}
