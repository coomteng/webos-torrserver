package subs

import (
	"archive/zip"
	"bytes"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Entry is one subtitle file, either standalone or taken out of a zip.
type Entry struct {
	Name string
	Data []byte
}

var subExtRe = regexp.MustCompile(`(?i)\.(srt|ass|ssa|vtt)$`)

// IsSubtitleName reports whether the file name has a subtitle extension.
func IsSubtitleName(n string) bool { return subExtRe.MatchString(n) }

// Limits on archives that come from the network. Variables so that tests can lower them.
var (
	maxArchiveBytes int64 = 20 << 20 // a downloaded package
	maxZipEntries         = 2000     // members looked at
	maxZipMember    int64 = 2 << 20  // one member, decompressed
	maxZipTotal     int64 = 32 << 20 // all members kept, decompressed
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
// members of a zip (nested directories flattened, __MACOSX junk skipped). Members larger than
// maxZipMember are skipped; it stops after maxZipEntries members or maxZipTotal bytes.
func Expand(name string, data []byte) []Entry {
	if archiveKind(data) == "zip" {
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

// unreadableMember is the error for a zip member that was found by name but could not be read:
// encrypted, corrupt or larger than maxZipMember.
type unreadableMember struct{ Name string }

func (e *unreadableMember) Error() string { return "zip: cannot read " + e.Name }

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
func zipSubBase(f *zip.File) (string, bool) {
	name := f.Name
	if f.NonUTF8 {
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
