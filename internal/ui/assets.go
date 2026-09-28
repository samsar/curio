package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// assetTypes are the Content-Types of the files static/ may hold. They are
// explicit rather than sniffed or read from the system's MIME table, and a
// file of any other kind fails New.
var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
}

// assetHashLen is how many hex digits of an asset's SHA-256 its URL
// carries: 64 bits, plenty to tell a handful of files' versions apart.
const assetHashLen = 16

// asset is one file of static/, served under its hashed name.
type asset struct {
	body        []byte
	contentType string
}

// assetSet is static/ by name and by hashed name.
type assetSet struct {
	hashed map[string]string // file name -> hashed name
	files  map[string]asset  // hashed name -> file
}

// loadAssets reads every file in dir of fsys and names each after its
// content: app.css becomes app.<first 16 hex digits of its SHA-256>.css.
// A URL then changes whenever its file does, so browsers can cache every
// asset for good.
func loadAssets(fsys fs.FS, dir string) (assetSet, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return assetSet{}, fmt.Errorf("read %s: %w", dir, err)
	}
	set := assetSet{hashed: map[string]string{}, files: map[string]asset{}}
	for _, e := range entries {
		if e.IsDir() {
			return assetSet{}, fmt.Errorf("%s/%s: assets are files, not directories", dir, e.Name())
		}
		ext := path.Ext(e.Name())
		contentType, ok := assetTypes[ext]
		if !ok {
			return assetSet{}, fmt.Errorf("%s/%s: no Content-Type for %q files", dir, e.Name(), ext)
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return assetSet{}, fmt.Errorf("read %s/%s: %w", dir, e.Name(), err)
		}
		sum := sha256.Sum256(body)
		name := strings.TrimSuffix(e.Name(), ext) + "." + hex.EncodeToString(sum[:])[:assetHashLen] + ext
		set.hashed[e.Name()] = name
		set.files[name] = asset{body: body, contentType: contentType}
	}
	return set, nil
}

// url is where the pages load the asset in static/ called name. It is an
// error, failing the page, for a name static/ doesn't have.
func (s assetSet) url(name string) (string, error) {
	hashed, ok := s.hashed[name]
	if !ok {
		return "", fmt.Errorf("no asset %q in static/", name)
	}
	return AssetPrefix + hashed, nil
}

// AssetPrefix is the path under which the pages load their assets.
const AssetPrefix = "/ui/static/"

// ServeAsset answers a request for the asset whose hashed name is file:
// its bytes, with its Content-Type and cached for a year, since the name
// changes with the content. It reports whether there is such an asset; a
// name it doesn't know, a stale hash included, is the caller's to answer.
func (r *Renderer) ServeAsset(w http.ResponseWriter, file string) bool {
	a, ok := r.assets.files[file]
	if !ok {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Content-Length", strconv.Itoa(len(a.body)))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.body) // fails only when the client has gone
	return true
}
