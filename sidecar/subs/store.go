package subs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Saved is one converted subtitle kept on disk for a torrent file: <dir>/<hash>/<index>/<n>.vtt + meta.
type Saved struct {
	N      int      `json:"n"`
	Name   string   `json:"name"`   // original file name
	Lang   []string `json:"lang"`   // tags
	Label  string   `json:"label"`  // 简中·英文
	Src    string   `json:"src"`    // assrt / upload
	Format string   `json:"format"` // srt / ass / vtt (original)
	Cues   int      `json:"cues"`
	Time   int64    `json:"time"`
	URL    string   `json:"url"` // filled by the handler: /subs/<hash>/<index>/<n>.vtt
}

// Store is the on-disk subtitle cache.
type Store struct {
	Dir string
	mu  sync.Mutex
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) dir(hash string, index int) (string, error) {
	if !hashRe.MatchString(hash) || index < 0 || index > 100000 {
		return "", errors.New("bad hash/index")
	}
	return filepath.Join(s.Dir, hash, strconv.Itoa(index)), nil
}

// List returns the saved subtitles for a torrent file, oldest first.
func (s *Store) List(hash string, index int) ([]Saved, error) {
	d, err := s.dir(hash, index)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMeta(d), nil
}

func (s *Store) readMeta(d string) []Saved {
	var out []Saved
	b, err := os.ReadFile(filepath.Join(d, "meta.json"))
	if err == nil {
		json.Unmarshal(b, &out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// Add converts raw subtitle bytes and stores them; returns the saved record.
func (s *Store) Add(hash string, index int, name, src string, raw []byte) (Saved, error) {
	d, err := s.dir(hash, index)
	if err != nil {
		return Saved{}, err
	}
	vtt, format, err := ToVTT(raw, filepath.Ext(name))
	if err != nil {
		return Saved{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(d, 0o755); err != nil {
		return Saved{}, err
	}
	list := s.readMeta(d)
	n := 1
	if len(list) > 0 {
		n = list[len(list)-1].N + 1
	}
	if err := os.WriteFile(filepath.Join(d, strconv.Itoa(n)+".vtt"), []byte(vtt), 0o644); err != nil {
		return Saved{}, err
	}
	lang := LangOf(name)
	rec := Saved{N: n, Name: name, Lang: lang, Label: LangLabel(lang), Src: src, Format: format,
		Cues: countCues(vtt), Time: time.Now().Unix()}
	list = append(list, rec)
	b, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(d, "meta.json"), b, 0o644); err != nil {
		return Saved{}, err
	}
	return rec, nil
}

// Delete removes one saved subtitle.
func (s *Store) Delete(hash string, index, n int) error {
	d, err := s.dir(hash, index)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.readMeta(d)
	var keep []Saved
	for _, r := range list {
		if r.N != n {
			keep = append(keep, r)
		}
	}
	if len(keep) == len(list) {
		return os.ErrNotExist
	}
	os.Remove(filepath.Join(d, strconv.Itoa(n)+".vtt"))
	b, _ := json.Marshal(keep)
	return os.WriteFile(filepath.Join(d, "meta.json"), b, 0o644)
}

// VTT returns the stored WebVTT text.
func (s *Store) VTT(hash string, index, n int) ([]byte, error) {
	d, err := s.dir(hash, index)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(d, strconv.Itoa(n)+".vtt"))
}

func countCues(vtt string) int {
	n := 0
	for i := 0; i+5 <= len(vtt); i++ {
		if vtt[i:i+5] == " --> " {
			n++
		}
	}
	return n
}
