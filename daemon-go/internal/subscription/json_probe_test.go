package subscription

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"ironlink/daemon/internal/store"
)

func TestJSONCandidateURL(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		format Format
		want   string
		ok     bool
	}{
		{"auto bare", "https://example.com/api/sub/TOKEN", FormatAuto, "https://example.com/api/sub/TOKEN/json", true},
		{"xray bare", "https://example.com/api/sub/TOKEN", FormatXray, "https://example.com/api/sub/TOKEN/json", true},
		{"trailing slash", "https://example.com/sub/TOKEN/", FormatAuto, "https://example.com/sub/TOKEN/json", true},
		{"query preserved", "https://example.com/sub/TOKEN?x=1", FormatAuto, "https://example.com/sub/TOKEN/json?x=1", true},
		{"already json", "https://example.com/sub/TOKEN/json", FormatAuto, "", false},
		{"already clash", "https://example.com/sub/TOKEN/clash", FormatAuto, "", false},
		{"links pin", "https://example.com/sub/TOKEN", FormatLinks, "", false},
		{"clash pin", "https://example.com/sub/TOKEN", FormatClash, "", false},
		{"singbox pin", "https://example.com/sub/TOKEN", FormatSingBox, "", false},
		{"garbage url", "://nope", FormatAuto, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := jsonCandidateURL(c.url, c.format)
			if ok != c.ok || got != c.want {
				t.Fatalf("jsonCandidateURL(%q,%v) = %q,%v; want %q,%v", c.url, c.format, got, ok, c.want, c.ok)
			}
		})
	}
}

// A valid single-object xray body carrying one node and a balancer, the shape
// Remnawave's /json endpoint delivers.
const xrayJSONBody = `{"outbounds":[` +
	`{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443,` +
	`"users":[{"id":"00000000-0000-4000-8000-000000000077","encryption":"none"}]}]},` +
	`"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"solo.example.com"}}}` +
	`],"routing":{"balancers":[{"tag":"b","selector":["proxy"]}]}}`

func TestFetchSubscriptionBodyPrefersJSON(t *testing.T) {
	base64Body := base64.StdEncoding.EncodeToString([]byte(linkA + "\n" + linkB))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub/TOKEN/json" {
			_, _ = w.Write([]byte(xrayJSONBody))
			return
		}
		_, _ = w.Write([]byte(base64Body)) // the bare URL: base64 links, no group
	}))
	defer srv.Close()

	sub := &store.Subscription{ID: "s1", URL: srv.URL + "/sub/TOKEN"}
	body, err := fetchSubscriptionBody(context.Background(), sub, FormatAuto, "test")
	if err != nil {
		t.Fatalf("fetchSubscriptionBody: %v", err)
	}
	o, err := Parse(body, sub.ID, FormatAuto)
	if err != nil {
		t.Fatalf("parse probed body: %v", err)
	}
	if o.Format != FormatXray || len(o.Groups) != 1 {
		t.Fatalf("probe should yield the xray body with its balancer group: format=%v groups=%d", o.Format, len(o.Groups))
	}
}

func TestFetchSubscriptionBodyFallsBackWhenNoJSON(t *testing.T) {
	base64Body := base64.StdEncoding.EncodeToString([]byte(linkA + "\n" + linkB))
	var jsonHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub/TOKEN/json" {
			jsonHits++
			http.Error(w, "not here", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(base64Body))
	}))
	defer srv.Close()

	sub := &store.Subscription{ID: "s1", URL: srv.URL + "/sub/TOKEN"}
	body, err := fetchSubscriptionBody(context.Background(), sub, FormatAuto, "test")
	if err != nil {
		t.Fatalf("fetchSubscriptionBody: %v", err)
	}
	if jsonHits != 1 {
		t.Fatalf("expected exactly one /json probe, got %d", jsonHits)
	}
	o, err := Parse(body, sub.ID, FormatAuto)
	if err != nil {
		t.Fatalf("parse fallback body: %v", err)
	}
	if o.Format != FormatLinks || len(o.Nodes) != 2 {
		t.Fatalf("fallback should be the bare base64 body: format=%v nodes=%d", o.Format, len(o.Nodes))
	}
}

// A pinned non-xray dialect must never probe /json.
func TestFetchSubscriptionBodyPinSkipsProbe(t *testing.T) {
	base64Body := base64.StdEncoding.EncodeToString([]byte(linkA))
	var jsonHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub/TOKEN/json" {
			jsonHits++
		}
		_, _ = w.Write([]byte(base64Body))
	}))
	defer srv.Close()

	sub := &store.Subscription{ID: "s1", URL: srv.URL + "/sub/TOKEN"}
	if _, err := fetchSubscriptionBody(context.Background(), sub, FormatLinks, "test"); err != nil {
		t.Fatalf("fetchSubscriptionBody: %v", err)
	}
	if jsonHits != 0 {
		t.Fatalf("a links pin must not probe /json, got %d hits", jsonHits)
	}
}
