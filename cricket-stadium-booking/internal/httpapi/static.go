package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"

	"stadiumbooking/web"
)

type staticAsset struct {
	contentType string
	cachePolicy string
	etag        string
	body        []byte
	gzipBody    []byte
}

var (
	indexAsset = mustStaticAsset("index.html", "text/html; charset=utf-8", "no-cache")
	appJSAsset = mustStaticAsset("app.js", "application/javascript; charset=utf-8", "public, max-age=0, must-revalidate")
)

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		// The app currently has no favicon. A dedicated empty response avoids
		// sending the complete HTML shell for the browser's automatic request.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	serveStaticAsset(w, r, indexAsset)
}

func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	serveStaticAsset(w, r, appJSAsset)
}

func mustStaticAsset(name, contentType, cachePolicy string) staticAsset {
	body, err := web.FS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("read embedded asset %s: %v", name, err))
	}

	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		panic(fmt.Sprintf("create gzip writer for %s: %v", name, err))
	}
	if _, err := writer.Write(body); err != nil {
		panic(fmt.Sprintf("compress embedded asset %s: %v", name, err))
	}
	if err := writer.Close(); err != nil {
		panic(fmt.Sprintf("finish compressed asset %s: %v", name, err))
	}

	digest := sha256.Sum256(body)
	return staticAsset{
		contentType: contentType,
		cachePolicy: cachePolicy,
		etag:        fmt.Sprintf(`W/"sha256-%x"`, digest),
		body:        body,
		gzipBody:    compressed.Bytes(),
	}
}

func serveStaticAsset(w http.ResponseWriter, r *http.Request, asset staticAsset) {
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("Cache-Control", asset.cachePolicy)
	w.Header().Set("ETag", asset.etag)
	addVary(w.Header(), "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), asset.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := asset.body
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body = asset.gzipBody
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
