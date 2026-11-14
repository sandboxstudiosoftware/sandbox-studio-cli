package auth

import (
	_ "embed"
	"encoding/base64"
	"html"
	"io"
	"net/http"
)

// logoPNG is the Sandbox Studio logo, embedded so the browser success/failure
// pages are self-contained (no runtime file dependency, works cross-platform).
//
//go:embed assets/logo.png
var logoPNG []byte

// logoDataURI is the embedded logo as a base64 data: URI, inlined directly into
// the branded pages. Inlining avoids a second HTTP request for the image, which
// matters because the loopback server shuts down immediately after it captures
// the token — a separate /logo.png request would race the shutdown and the image
// would fail to load.
var logoDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)

// logoPath is a defensive fallback: the branded pages inline the logo as a data
// URI (logoDataURI), but the mux also serves the raw PNG here so the logo still
// loads in the rare environment that restricts large data: URIs.
const logoPath = "/logo.png"

// serveLogo writes the embedded logo PNG.
func serveLogo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(logoPNG)
}

// writeBrandedPage renders a centered page with the Sandbox Studio logo above the
// given message. The message is HTML-escaped. The logo is inlined as a data URI
// so it renders even as the loopback server is shutting down.
func writeBrandedPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sandbox Studio CLI</title>
<style>
  html, body { height: 100%; margin: 0; }
  body {
    display: flex;
    align-items: center;
    justify-content: center;
    background: #ffffff;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    color: #1a1f3d;
  }
  .card {
    text-align: center;
    padding: 2.5rem 3rem;
  }
  .card img {
    width: 260px;
    max-width: 70vw;
    height: auto;
    margin-bottom: 1.75rem;
  }
  .card p {
    font-size: 1.1rem;
    line-height: 1.5;
    margin: 0;
  }
</style>
</head>
<body>
  <div class="card">
    <img src="` + logoDataURI + `" alt="Sandbox Studio">
    <p>` + html.EscapeString(message) + `</p>
  </div>
</body>
</html>`
	_, _ = io.WriteString(w, page)
}
