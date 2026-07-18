package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var gzipWriters = sync.Pool{
	New: func() any {
		writer, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			panic(err) // gzip.BestSpeed is a compile-time-valid level.
		}
		return writer
	},
}

// gzipMiddleware compresses the text resources served by this application
// when the client supports it. Writers are pooled and use BestSpeed because
// the high-concurrency seat-polling path benefits more from low CPU cost than
// from squeezing the final few bytes out of a small JSON document.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}

		wrapped := &gzipResponseWriter{ResponseWriter: w, request: r}
		next.ServeHTTP(wrapped, r)
		wrapped.close()
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	request     *http.Request
	writer      *gzip.Writer
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true

	if w.request.Method != http.MethodHead && statusAllowsBody(status) &&
		w.Header().Get("Content-Encoding") == "" && compressibleContentType(w.Header().Get("Content-Type")) {
		addVary(w.Header(), "Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		w.writer = gzipWriters.Get().(*gzip.Writer)
		w.writer.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.writer != nil {
		return w.writer.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *gzipResponseWriter) close() {
	if w.writer == nil {
		return
	}
	_ = w.writer.Close()
	w.writer.Reset(io.Discard)
	gzipWriters.Put(w.writer)
	w.writer = nil
}

func statusAllowsBody(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

func compressibleContentType(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/javascript") ||
		strings.HasPrefix(contentType, "text/html")
}

func acceptsGzip(header string) bool {
	wildcardQuality := -1.0
	for _, part := range strings.Split(header, ",") {
		parameters := strings.Split(part, ";")
		encoding := strings.ToLower(strings.TrimSpace(parameters[0]))
		quality := 1.0
		for _, parameter := range parameters[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) != 2 || !strings.EqualFold(keyValue[0], "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(keyValue[1], 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if encoding == "gzip" {
			return quality > 0
		}
		if encoding == "*" {
			wildcardQuality = quality
		}
	}
	return wildcardQuality > 0
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, token := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

// etagMatches applies the weak comparison required for If-None-Match on GET
// and HEAD. Validators are deliberately weak because gzip and identity
// encodings carry the same semantic representation.
func etagMatches(ifNoneMatch, current string) bool {
	current = opaqueETag(current)
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || opaqueETag(candidate) == current && current != "" {
			return true
		}
	}
	return false
}

func opaqueETag(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.EqualFold(value[:2], "W/") {
		return strings.TrimSpace(value[2:])
	}
	return value
}
