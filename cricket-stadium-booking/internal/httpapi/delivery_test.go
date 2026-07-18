package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func requestWithHeaders(t *testing.T, handler http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func gunzipBody(t *testing.T, recorder *httptest.ResponseRecorder) []byte {
	t.Helper()
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatalf("open gzip response: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip response: %v", err)
	}
	return body
}

func TestStaticAssets_CacheAndCompress(t *testing.T) {
	handler := NewServer(nil, false)

	index := requestWithHeaders(t, handler, http.MethodGet, "/", nil)
	if index.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", index.Code)
	}
	if got := index.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", got)
	}
	if got := index.Header().Get("ETag"); got == "" {
		t.Fatal("index ETag is empty")
	}
	if got := index.Body.Bytes(); string(got) != string(indexAsset.body) {
		t.Fatal("identity index differs from embedded source")
	}
	if got := index.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("index Vary = %q, want Accept-Encoding", got)
	}

	notModified := requestWithHeaders(t, handler, http.MethodGet, "/", map[string]string{
		"If-None-Match": index.Header().Get("ETag"),
	})
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("conditional index status = %d, want 304", notModified.Code)
	}
	if notModified.Body.Len() != 0 {
		t.Errorf("conditional index body length = %d, want 0", notModified.Body.Len())
	}

	appIdentity := requestWithHeaders(t, handler, http.MethodGet, "/app.js", nil)
	if appIdentity.Code != http.StatusOK {
		t.Fatalf("identity app.js status = %d, want 200", appIdentity.Code)
	}
	if got := appIdentity.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("identity app.js Content-Encoding = %q, want empty", got)
	}
	if got := appIdentity.Body.Bytes(); string(got) != string(appJSAsset.body) {
		t.Fatal("identity app.js differs from embedded source")
	}

	appJS := requestWithHeaders(t, handler, http.MethodGet, "/app.js", map[string]string{
		"Accept-Encoding": "br, gzip",
	})
	if appJS.Code != http.StatusOK {
		t.Fatalf("app.js status = %d, want 200", appJS.Code)
	}
	if got := appJS.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("app.js Content-Encoding = %q, want gzip", got)
	}
	if got := appJS.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("app.js Vary = %q, want Accept-Encoding", got)
	}
	if got, want := appJS.Body.Len(), len(appJSAsset.gzipBody); got != want {
		t.Errorf("compressed app.js length = %d, want precompressed length %d (possible double compression)", got, want)
	}
	if got := gunzipBody(t, appJS); string(got) != string(appJSAsset.body) {
		t.Fatal("decompressed app.js differs from embedded source")
	}

	head := requestWithHeaders(t, handler, http.MethodHead, "/app.js", map[string]string{
		"Accept-Encoding": "gzip",
	})
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD app.js = status %d, body length %d; want 200 and empty", head.Code, head.Body.Len())
	}
	if head.Header().Get("Content-Encoding") != "gzip" {
		t.Error("HEAD app.js does not describe the gzip representation")
	}
}

func TestFavicon_DoesNotFallBackToHTML(t *testing.T) {
	handler := NewServer(nil, false)
	recorder := requestWithHeaders(t, handler, http.MethodGet, "/favicon.ico", nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("favicon status = %d, want 204", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("favicon body length = %d, want 0", recorder.Body.Len())
	}
}

func TestJSON_GzipNegotiation(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"message": strings.Repeat("seat-map-", 100)})
	}))

	compressed := requestWithHeaders(t, handler, http.MethodGet, "/data", map[string]string{
		"Accept-Encoding": "gzip",
	})
	if got := compressed.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("JSON Content-Encoding = %q, want gzip", got)
	}
	if got := compressed.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("JSON Cache-Control = %q, want no-store", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(gunzipBody(t, compressed), &payload); err != nil {
		t.Fatalf("decode compressed JSON: %v", err)
	}
	if payload["message"] == "" {
		t.Error("compressed JSON lost its payload")
	}

	identity := requestWithHeaders(t, handler, http.MethodGet, "/data", map[string]string{
		"Accept-Encoding": "gzip;q=0, *;q=1",
	})
	if got := identity.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("gzip;q=0 Content-Encoding = %q, want identity", got)
	}
}

func TestETagMatches_WeakAndLists(t *testing.T) {
	current := `W/"seats-abc"`
	for _, header := range []string{`W/"seats-abc"`, `"seats-abc"`, `"other", W/"seats-abc"`, `*`} {
		if !etagMatches(header, current) {
			t.Errorf("etagMatches(%q, %q) = false, want true", header, current)
		}
	}
	if etagMatches(`W/"seats-other"`, current) {
		t.Error("different ETag matched")
	}
}
