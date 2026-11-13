// Package importer hands finished rips to where they belong. The Radarr
// importer imports them through Radarr's own Manual Import (decision D9);
// the library importer (v0.2) will rename and move them itself.
package importer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// PathMap translates a path as Discern sees it into the same path as Radarr
// sees it. Both usually run in containers that mount the same storage at
// different places: Discern's staging folder /media/staging may be
// /data/staging inside Radarr. Radarr can only import a path it can open.
type PathMap struct {
	Local  string // a folder as Discern sees it, e.g. "/media/staging"
	Remote string // the same folder as Radarr sees it, e.g. "/data/staging"
}

// ToRemote maps p, which must be Local or inside it. Anything outside is an
// error, never passed through: this is also the guard that keeps Discern
// from asking Radarr to import files from anywhere else.
func (m PathMap) ToRemote(p string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(m.Local), filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q is outside the staging folder %q", p, m.Local)
	}
	// Radarr runs on Linux: its paths use forward slashes.
	return path.Join(m.Remote, filepath.ToSlash(rel)), nil
}
