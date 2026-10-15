package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.34.0", "v0.34.1", true},
		{"v0.34.0", "v0.35.0", true},
		{"v0.34.0", "v1.0.0", true},
		{"v0.34.0", "v0.34.0", false},
		{"v0.35.0", "v0.34.9", false},
		{"v0.9.0", "v0.10.0", true},
		{"v0.35.0-rc.1", "v0.35.0", true},
		{"v0.35.0", "v0.35.0-rc.1", false},
		{"0.34.0", "v0.34.1", true},
		{"abc1234", "v0.34.1", false},
		{"v0.34.0", "latest", false},
		{"v0.34.0", "v0.34", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestLatestReadsTheTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); accept != "application/vnd.github+json" {
			t.Errorf("Accept = %q", accept)
		}
		w.Write([]byte(`{"tag_name":"v0.35.0","name":"v0.35.0"}`))
	}))
	defer server.Close()

	tag, err := Latest(context.Background(), server.Client(), server.URL)
	if err != nil || tag != "v0.35.0" {
		t.Fatalf("Latest() = (%q, %v), want v0.35.0", tag, err)
	}
}

func TestLatestRejectsFailuresAndMalformedTags(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rate limited", http.StatusForbidden) },
		"json":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{`)) },
		"tag":    func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"tag_name":"nightly"}`)) },
	} {
		server := httptest.NewServer(handler)
		if tag, err := Latest(context.Background(), server.Client(), server.URL); err == nil {
			t.Errorf("%s: Latest() = %q, want an error", name, tag)
		}
		server.Close()
	}
}
