package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func s3Fixture(t *testing.T, policy, grants string) (*S3, error) {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == "HEAD":
			w.WriteHeader(200)
		case r.URL.Query().Has("policy"):
			if policy == "" {
				w.WriteHeader(404)
				io.WriteString(w, `<Error><Code>NoSuchBucketPolicy</Code></Error>`)
			} else {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, policy)
			}
		case r.URL.Query().Has("acl"):
			io.WriteString(w, `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList>`+grants+`</AccessControlList></AccessControlPolicy>`)
		case r.URL.Query().Has("cors"):
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "https://bf.test") || strings.Contains(string(body), "<AllowedOrigin>*</AllowedOrigin>") {
				t.Error("invalid CORS", string(body))
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
		}
	}))
	t.Cleanup(h.Close)
	t.Setenv("RUSTFS_ENDPOINT_URL", h.URL)
	t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", "https://objects.bf.test")
	t.Setenv("RUSTFS_ACCESS_KEY_ID", "test-access")
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("MOMENTS_BUCKET", "private-moments")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return New(ctx, "https://bf.test")
}
func TestPrivateBucketAndExternalSigning(t *testing.T) {
	s, err := s3Fixture(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	upload, headers, err := s.Upload(context.Background(), "staging/key", "image/png", 123)
	if err != nil || headers["Content-Type"] != "image/png" {
		t.Fatal(headers, err)
	}
	u, _ := url.Parse(upload)
	if u.Host != "objects.bf.test" || u.Path != "/private-moments/staging/key" || u.Query().Get("X-Amz-Expires") != "600" {
		t.Fatal(u)
	}
	download, err := s.Download(context.Background(), "sealed/key")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(download)
	if u.Path != "/private-moments/sealed/key" || u.Query().Get("X-Amz-Expires") != "60" {
		t.Fatal(u)
	}
}
func TestRejectExistingPolicyOrPublicACL(t *testing.T) {
	for _, test := range []struct{ policy, grants string }{{`{"Statement":[]}`, ""}, {"", `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`}} {
		t.Run(test.policy+test.grants, func(t *testing.T) {
			if _, err := s3Fixture(t, test.policy, test.grants); err == nil {
				t.Fatal("public bucket allowed")
			}
		})
	}
}
func TestRejectInvalidEndpoints(t *testing.T) {
	for _, endpoint := range []string{"file:///tmp", "https://user:pass@host", "https://host?secret=x", "not-url"} {
		if _, err := client(endpoint); err == nil {
			t.Fatal(endpoint)
		}
	}
}
