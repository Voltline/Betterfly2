package call

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
)

type StaticICEProvider struct {
	STUNURLs      []string
	TURNURLs      []string
	TURNSecret    string
	CredentialTTL time.Duration
}

func NewStaticICEProvider(stunURLs, turnURLs, turnSecret string, credentialTTL time.Duration) *StaticICEProvider {
	if credentialTTL <= 0 {
		credentialTTL = time.Hour
	}
	return &StaticICEProvider{
		STUNURLs:      splitCSV(stunURLs),
		TURNURLs:      splitCSV(turnURLs),
		TURNSecret:    turnSecret,
		CredentialTTL: credentialTTL,
	}
}

func (p *StaticICEProvider) Servers(userID int64, now time.Time) []*callpb.IceServer {
	servers := make([]*callpb.IceServer, 0, 2)
	if len(p.STUNURLs) > 0 {
		servers = append(servers, &callpb.IceServer{Urls: p.STUNURLs})
	}
	if len(p.TURNURLs) == 0 || p.TURNSecret == "" {
		return servers
	}

	username := fmt.Sprintf("%d:%d", now.Add(p.CredentialTTL).Unix(), userID)
	mac := hmac.New(sha1.New, []byte(p.TURNSecret))
	_, _ = mac.Write([]byte(username))
	servers = append(servers, &callpb.IceServer{
		Urls:       p.TURNURLs,
		Username:   username,
		Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	})
	return servers
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
