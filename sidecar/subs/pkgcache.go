package subs

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// PkgCache keeps on disk what was downloaded from a subtitle site, so that it is downloaded once.
// A season pack holds every episode in every language: the first episode fetches it, the others
// are read from here. One directory per package id:
//
//	<Dir>/<id>/list.json       the package's file names, as the site lists them
//	<Dir>/<id>/archive         the whole package, if it was downloaded
//	<Dir>/<id>/f/<sha1(name)>  single files, if they were downloaded on their own
//
// Nothing expires: a published package does not change. When the directory grows past Max bytes,
// the packages used longest ago are removed. A nil *PkgCache keeps nothing.
type PkgCache struct {
	Dir string
	Max int64

	mu sync.Mutex
}

// PkgList is the file list of a package.
type PkgList struct {
	Archive string   `json:"archive"` // the archive's file name; "" if the package is one plain file
	Files   []string `json:"files"`
}

func NewPkgCache(dir string) *PkgCache { return &PkgCache{Dir: dir, Max: 200 << 20} }

var pkgIDRe = regexp.MustCompile(`^[0-9A-Za-z]{1,32}$`)

// path returns the place of a part of package id, or "" if there is no cache or id cannot be a
// directory name.
func (c *PkgCache) path(id string, part ...string) string {
	if c == nil || c.Dir == "" || !pkgIDRe.MatchString(id) {
		return ""
	}
	return filepath.Join(append([]string{c.Dir, id}, part...)...)
}

func fileKey(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

func (c *PkgCache) get(id string, part ...string) ([]byte, bool) {
	p := c.path(id, part...)
	if p == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	now := time.Now()
	os.Chtimes(c.path(id), now, now) // used just now: last in line for removal
	return b, true
}

func (c *PkgCache) put(data []byte, id string, part ...string) {
	p := c.path(id, part...)
	if p == "" || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, p) != nil {
		os.Remove(tmp)
		return
	}
	now := time.Now()
	os.Chtimes(c.path(id), now, now)
	c.trim(id)
}

// trim removes the packages used longest ago until the cache fits Max; never the package keep.
func (c *PkgCache) trim(keep string) {
	type pkg struct {
		dir  string
		size int64
		used time.Time
	}
	var all []pkg
	var total int64
	dirs, _ := os.ReadDir(c.Dir)
	for _, d := range dirs {
		info, err := d.Info()
		if err != nil || !d.IsDir() {
			continue
		}
		p := pkg{dir: filepath.Join(c.Dir, d.Name()), used: info.ModTime()}
		filepath.Walk(p.dir, func(_ string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				p.size += fi.Size()
			}
			return nil
		})
		total += p.size
		if d.Name() != keep {
			all = append(all, p)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	for _, p := range all {
		if total <= c.Max {
			return
		}
		if os.RemoveAll(p.dir) == nil {
			total -= p.size
		}
	}
}

// List returns the stored file list of a package.
func (c *PkgCache) List(id string) (PkgList, bool) {
	var l PkgList
	b, ok := c.get(id, "list.json")
	if !ok || json.Unmarshal(b, &l) != nil || len(l.Files) == 0 {
		return PkgList{}, false
	}
	return l, true
}

func (c *PkgCache) PutList(id string, l PkgList) {
	if b, err := json.Marshal(l); err == nil && len(l.Files) > 0 {
		c.put(b, id, "list.json")
	}
}

// Archive returns the stored whole package.
func (c *PkgCache) Archive(id string) ([]byte, bool) { return c.get(id, "archive") }

func (c *PkgCache) PutArchive(id string, data []byte) { c.put(data, id, "archive") }

// File returns a stored single file of the package.
func (c *PkgCache) File(id, name string) ([]byte, bool) { return c.get(id, "f", fileKey(name)) }

func (c *PkgCache) PutFile(id, name string, data []byte) { c.put(data, id, "f", fileKey(name)) }
