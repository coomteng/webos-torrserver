package subs

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwaples/rardecode/v2"
)

// Entry is one subtitle file, either standalone or taken out of a zip or rar.
type Entry struct {
	Name string
	Data []byte
}

var subExtRe = regexp.MustCompile(`(?i)\.(srt|ass|ssa|vtt)$`)

// IsSubtitleName reports whether the file name has a subtitle extension.
func IsSubtitleName(n string) bool { return subExtRe.MatchString(n) }

// Limits on archives that come from the network; the maxZip ones hold for rar too. Variables so
// that tests can lower them.
var (
	maxArchiveBytes int64 = 20 << 20 // a downloaded package
	maxZipEntries         = 2000     // members looked at
	maxZipMember    int64 = 2 << 20  // one member, decompressed
	maxZipTotal     int64 = 32 << 20 // all members kept, decompressed
	maxRarDict      int64 = 64 << 20 // rar decompression window (WinRAR's default is 32 MB)
	rarTimeout            = 10 * time.Second
)

// archiveKind tells a zip, rar or 7z archive by its magic; "" for anything else.
func archiveKind(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07")):
		return "rar"
	case bytes.HasPrefix(data, []byte("7z\xbc\xaf\x27\x1c")):
		return "7z"
	}
	return ""
}

// Expand returns the subtitle files inside data: the file itself, or the .srt/.ass/.ssa/.vtt
// members of a zip or rar (nested directories flattened, __MACOSX junk skipped). Members larger
// than maxZipMember are skipped; it stops after maxZipEntries members or maxZipTotal bytes.
func Expand(name string, data []byte) []Entry {
	switch archiveKind(data) {
	case "rar":
		var out []Entry
		var total int64
		err := rarWalk(data, func(h *rardecode.FileHeader, r io.Reader) bool {
			base, ok := subBase(h.Name, !utf8.ValidString(h.Name))
			if !ok {
				return true
			}
			b := rarRead(h, r)
			if len(b) == 0 {
				return true
			}
			if total += int64(len(b)); total > maxZipTotal {
				return false
			}
			out = append(out, Entry{Name: base, Data: b})
			return true
		})
		if err == nil || len(out) > 0 { // a rar that breaks off keeps the members before the break
			return out
		}
	case "zip":
		if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
			var out []Entry
			var total int64
			for i, f := range zr.File {
				if i >= maxZipEntries {
					break
				}
				base, ok := zipSubName(f)
				if !ok {
					continue
				}
				b := zipRead(f)
				if len(b) == 0 {
					continue
				}
				if total += int64(len(b)); total > maxZipTotal {
					break
				}
				out = append(out, Entry{Name: base, Data: b})
			}
			return out
		}
	}
	if IsSubtitleName(name) || name == "" {
		return []Entry{{Name: name, Data: data}}
	}
	// unknown extension but maybe still a subtitle (phones strip names): let the parser decide
	return []Entry{{Name: name, Data: data}}
}

// unreadableMember is the error for an archive member that was found by name but could not be
// read: encrypted, corrupt or larger than maxZipMember.
type unreadableMember struct{ Name string }

func (e *unreadableMember) Error() string { return "archive: cannot read " + e.Name }

// zipMember looks in the zip for the subtitle member whose base name is want (exactly, else
// ignoring case) and decompresses only that one. subtitles counts the subtitle members seen,
// so that a miss can be told from an archive without any. A member that is found but cannot be
// read is an *unreadableMember error, not a miss.
func zipMember(data []byte, want string) (hit *Entry, subtitles int, err error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, err
	}
	var exact, fold *zip.File
	for i, f := range zr.File {
		if i >= maxZipEntries {
			break
		}
		base, ok := zipSubBase(f)
		if !ok {
			continue
		}
		if f.UncompressedSize64 <= uint64(maxZipMember) {
			subtitles++
		}
		switch {
		case base == want && exact == nil:
			exact = f
		case strings.EqualFold(base, want) && fold == nil:
			fold = f
		}
	}
	if exact == nil {
		exact = fold
	}
	if exact != nil {
		base, _ := zipSubBase(exact)
		var b []byte
		if exact.UncompressedSize64 <= uint64(maxZipMember) {
			b = zipRead(exact)
		}
		if b == nil {
			return nil, subtitles, &unreadableMember{Name: base}
		}
		if len(b) > 0 {
			hit = &Entry{Name: base, Data: b}
		}
	}
	return hit, subtitles, nil
}

// zipSubName returns the member's base name and whether it is a subtitle worth reading: a
// subtitle extension, not __MACOSX junk or a dot file, not declared larger than maxZipMember.
// Member names are CP437 unless the UTF-8 flag is set; Chinese archives are usually GBK. Archives
// made on Windows may separate directories with a backslash.
func zipSubName(f *zip.File) (string, bool) {
	base, ok := zipSubBase(f)
	return base, ok && f.UncompressedSize64 <= uint64(maxZipMember)
}

// zipSubBase is zipSubName without the size check.
func zipSubBase(f *zip.File) (string, bool) { return subBase(f.Name, f.NonUTF8) }

// subBase returns an archive member's base name and whether it names a subtitle; a name that is
// not UTF-8 is decoded as GBK or Big5.
func subBase(name string, nonUTF8 bool) (string, bool) {
	if nonUTF8 {
		name = Decode([]byte(name))
	}
	name = strings.ReplaceAll(name, "\\", "/")
	base := path.Base(name)
	return base, IsSubtitleName(base) && !strings.HasPrefix(name, "__MACOSX") && !strings.HasPrefix(base, ".")
}

// zipRead decompresses a member; nil if it cannot be read or turns out larger than maxZipMember
// (the declared size may lie), rather than a truncated subtitle.
func zipRead(f *zip.File) []byte {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxZipMember+1))
	if err != nil || int64(len(b)) > maxZipMember {
		return nil
	}
	return b
}

var errRarTimeout = errors.New("rar: took too long")

// rarMu lets one rar be decoded at a time: a decoder may hold its window and model in memory.
var rarMu sync.Mutex

// rarInput hands the archive to the decoder and fails once the deadline has passed, which stops
// the decoder wherever it is: also while it decompresses members only to skip them, as it must
// in a solid archive.
type rarInput struct {
	*bytes.Reader
	deadline time.Time
}

func (in *rarInput) Read(p []byte) (int, error) {
	if time.Now().After(in.deadline) {
		return 0, errRarTimeout
	}
	return in.Reader.Read(p)
}

// rarWalk calls visit for the files of a rar (RAR 1.5 to 7, one volume) in archive order, at most
// maxZipEntries members, until visit returns false; r reads the member's contents. The names are
// UTF-8, except RAR 4 names stored without the Unicode flag, which are the bytes as archived.
// Encrypted headers, a volume that continues elsewhere, corrupt data, a window larger than
// maxRarDict and running out of rarTimeout are errors.
func rarWalk(data []byte, visit func(h *rardecode.FileHeader, r io.Reader) bool) (err error) {
	rarMu.Lock()
	defer rarMu.Unlock()
	defer func() {
		if recover() != nil { // the decoder is fed bytes from the network
			err = errors.New("rar: corrupt archive")
		}
	}()
	in := &rarInput{Reader: bytes.NewReader(data), deadline: time.Now().Add(rarTimeout)}
	// a small read buffer, so that the deadline is looked at often
	rr, err := rardecode.NewReader(in, rardecode.MaxDictionarySize(maxRarDict), rardecode.BufferSize(256))
	if err != nil {
		return err
	}
	for i := 0; i < maxZipEntries; i++ {
		h, err := rr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !h.IsDir && !visit(h, rr) {
			return nil
		}
	}
	return nil
}

// rarFits reports whether the member does not declare more than maxZipMember bytes.
func rarFits(h *rardecode.FileHeader) bool { return h.UnKnownSize || h.UnPackedSize <= maxZipMember }

// rarRead decompresses the current member, reading at most maxZipMember+1 bytes; nil if it is
// encrypted, cannot be read (the checksum is verified) or is larger than maxZipMember.
func rarRead(h *rardecode.FileHeader, r io.Reader) []byte {
	if h.Encrypted || !rarFits(h) {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r, maxZipMember+1))
	if err != nil || int64(len(b)) > maxZipMember {
		return nil
	}
	return b
}

// rarMember is zipMember for a rar. A rar is read front to back, so it stops at the member
// named exactly want and reads the first one named so ignoring case as it passes it. An archive
// that breaks off is an error unless the member was found before the break.
func rarMember(data []byte, want string) (hit *Entry, subtitles int, err error) {
	var fold *Entry // Data is nil if the member could not be read
	exact := false
	err = rarWalk(data, func(h *rardecode.FileHeader, r io.Reader) bool {
		base, ok := subBase(h.Name, !utf8.ValidString(h.Name))
		if !ok {
			return true
		}
		if rarFits(h) {
			subtitles++
		}
		switch {
		case base == want:
			fold, exact = &Entry{Name: base, Data: rarRead(h, r)}, true
		case strings.EqualFold(base, want) && fold == nil:
			fold = &Entry{Name: base, Data: rarRead(h, r)}
		}
		return !exact
	})
	switch {
	case fold == nil:
		return nil, subtitles, err
	case fold.Data == nil:
		return nil, subtitles, &unreadableMember{Name: fold.Name}
	case len(fold.Data) > 0:
		hit = fold
	}
	return hit, subtitles, nil
}

// Lang tags recognised in file names and ASSRT langlist keys, in display order.
var langLabels = []struct{ key, label string }{
	{"chs", "简中"}, {"cht", "繁中"}, {"eng", "英文"}, {"dou", "双语"}, {"jap", "日文"}, {"kor", "韩文"},
}

// Tags are often glued to the previous token ("WEB-DLChsEng.srt"), so chs/cht match anywhere;
// "eng" needs a boundary or CamelCase to stay clear of "Avengers".
var langHintRe = map[string]*regexp.Regexp{
	"dou": regexp.MustCompile(`(?i)双语|chs&eng|cht&eng|chs\.?eng|cht\.?eng|简英|繁英|中英|zh-?en`),
	"chs": regexp.MustCompile(`(?i)chs|简体|简中|zh-?cn|\bsc\b|\bgb\b|\.cn(\.|$)|简`),
	"cht": regexp.MustCompile(`(?i)cht|繁体|繁體|繁中|zh-?tw|zh-?hk|\bbig5\b|\.(tw|hk)(\.|$)|繁`),
	"eng": regexp.MustCompile(`(?i:\beng?\b|english|英文|英)|Eng(?:[A-Z.]|$)`),
	"jap": regexp.MustCompile(`(?i)\bjpn?\b|\bjap\b|日文|日语`),
	"kor": regexp.MustCompile(`(?i)\bkor?\b|韩文|韓文`),
}

// LangOf guesses language tags from a file name ("chs", "cht", "eng", "dou", …).
func LangOf(name string) []string {
	var out []string
	for _, l := range langLabels {
		if re := langHintRe[l.key]; re != nil && re.MatchString(name) {
			out = append(out, l.key)
		}
	}
	// "chs" also matches "chs&eng"; collapse to dou + chs so the label reads 简中 双语
	return out
}

// LangLabel renders tags as a short Chinese label: ["chs","eng","dou"] -> "简中·英文·双语".
func LangLabel(tags []string) string {
	var parts []string
	for _, l := range langLabels {
		for _, t := range tags {
			if t == l.key {
				parts = append(parts, l.label)
			}
		}
	}
	return strings.Join(parts, "·")
}

// Pick chooses the best subtitle among several for a given video file name: same episode first,
// then Simplified Chinese > bilingual > Traditional > English, then .srt over .ass, then larger file.
func Pick(entries []Entry, videoName string) *Entry {
	if len(entries) == 0 {
		return nil
	}
	ep, hasEp := ParseEpisode(videoName)
	scoreOf := func(e Entry) int {
		s := 0
		if hasEp {
			if e2, ok := ParseEpisode(e.Name); ok && e2.Number == ep.Number && (e2.Season == ep.Season || e2.Season == 0 || ep.Season == 0) {
				s += 1000
			} else if ok {
				s -= 1000
			}
		}
		tags := map[string]bool{}
		for _, t := range LangOf(e.Name) {
			tags[t] = true
		}
		switch {
		case tags["dou"]:
			s += 80 // bilingual files also match chs/cht; count them once, below pure Simplified
		case tags["chs"]:
			s += 100
		case tags["cht"]:
			s += 40
		}
		if tags["eng"] && !tags["dou"] {
			s += 10
		}
		if strings.HasSuffix(strings.ToLower(e.Name), ".srt") {
			s += 5
		}
		return s
	}
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := scoreOf(entries[idx[a]]), scoreOf(entries[idx[b]])
		if sa != sb {
			return sa > sb
		}
		return len(entries[idx[a]].Data) > len(entries[idx[b]].Data)
	})
	return &entries[idx[0]]
}
