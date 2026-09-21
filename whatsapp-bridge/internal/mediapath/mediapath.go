// Package mediapath is the one gate between a stored or requested path and a file on disk
// under store/media/.
//
// It serves two callers that must agree exactly: GET /api/media/ streams what it resolves,
// and retention deletes what it resolves. A second copy of this logic in either place would
// be a second chance to get it wrong, and for retention "wrong" means deleting the WhatsApp
// session database — which a symlink disguised as audio would otherwise point at.
package mediapath

import (
	"os"
	"path/filepath"
	"strings"
)

// servableTypes maps the only file extensions that may be served — or deleted — to their
// content types.
var servableTypes = map[string]string{
	".ogg": "audio/ogg; codecs=opus",
	".mp3": "audio/mpeg",
}

// Resolve maps a subpath to a regular file inside mediaRoot. It rejects traversal,
// absolute paths, symlinks leaving mediaRoot, and any extension other than .ogg/.mp3, so the
// databases and other files in the store directory can never be served.
func Resolve(mediaRoot, subpath string) (string, string, bool) {
	if subpath == "" || strings.Contains(subpath, "\\") {
		return "", "", false
	}
	cleanPath := filepath.Clean(subpath)
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return "", "", false
	}
	contentType, ok := servableTypes[strings.ToLower(filepath.Ext(cleanPath))]
	if !ok {
		return "", "", false
	}

	rootAbs, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return "", "", false
	}
	targetAbs, err := filepath.EvalSymlinks(filepath.Join(mediaRoot, cleanPath))
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	if fi, err := os.Stat(targetAbs); err != nil || !fi.Mode().IsRegular() {
		return "", "", false
	}
	return targetAbs, contentType, true
}
