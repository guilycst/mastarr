// Package assets serves the BFF-owned, public-safe preview asset.
package assets

import (
	_ "embed"
	"net/http"
)

// PreviewPath is the only application-owned static asset exposed by this
// package. It contains no inventory, host, path or deployment data.
const PreviewPath = "/preview.svg"

const (
	PreviewMIMEType = "image/svg+xml"
	PreviewWidth    = 1200
	PreviewHeight   = 630
	PreviewAlt      = "Mastarr operations dashboard"
)

//go:embed preview.svg
var previewSVG []byte

// Handler returns an immutable handler for the generic social preview.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PreviewPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", PreviewMIMEType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", stringSize(len(previewSVG)))
		_, _ = w.Write(previewSVG)
	})
}

// stringSize keeps the handler independent of fmt and avoids converting any
// request-controlled data. net/http accepts a decimal Content-Length value.
func stringSize(size int) string {
	if size == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	for size > 0 {
		i--
		buf[i] = byte('0' + size%10)
		size /= 10
	}
	return string(buf[i:])
}
