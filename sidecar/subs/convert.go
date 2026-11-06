package subs

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func atoi(s string) int                         { n, _ := strconv.Atoi(s); return n }
func sprintf(f string, a ...interface{}) string { return fmt.Sprintf(f, a...) }

// Cue is one subtitle line span in milliseconds.
type Cue struct {
	Start, End int
	Text       string // lines joined with "\n", no markup except <i>/<b>
}

// ToVTT converts SRT, ASS/SSA or VTT bytes in any common encoding to UTF-8 WebVTT.
// Returns the VTT text, the detected format ("srt"|"ass"|"vtt") and an error if nothing parsed.
func ToVTT(raw []byte, hint string) (string, string, error) {
	text := Decode(raw)
	format := sniff(text, hint)
	var cues []Cue
	switch format {
	case "ass":
		cues = parseASS(text)
	case "vtt":
		cues = parseSRT(stripVTTHeader(text), true)
	default:
		cues = parseSRT(text, false)
		format = "srt"
	}
	if len(cues) == 0 {
		return "", format, errors.New("no subtitle cues found")
	}
	return RenderVTT(cues), format, nil
}

// Decode guesses the text encoding (BOMs, UTF-8, GB18030, Big5) and returns UTF-8 without BOM.
func Decode(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:])
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return decodeWith(raw[2:], unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM))
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return decodeWith(raw[2:], unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM))
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	// Chinese subtitle files without BOM are overwhelmingly GBK (GB18030 is a superset); Big5 for the
	// traditional ones. Pick the decoder that produces fewer replacement chars / more common hanzi.
	gb := decodeWith(raw, simplifiedchinese.GB18030)
	b5 := decodeWith(raw, traditionalchinese.Big5)
	if score(b5) > score(gb) {
		return b5
	}
	return gb
}

func decodeWith(raw []byte, enc encoding.Encoding) string {
	out, _, err := transform.Bytes(enc.NewDecoder(), raw)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// score: common CJK punctuation/hanzi count minus replacement chars and private-use garbage.
func score(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r == utf8.RuneError || (r >= 0xE000 && r <= 0xF8FF):
			n -= 3
		case r >= 0x4E00 && r <= 0x9FFF, r == '，', r == '。', r == '？', r == '！':
			n++
		}
	}
	return n
}

func sniff(text, hint string) string {
	h := strings.ToLower(strings.TrimPrefix(hint, "."))
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	switch {
	case strings.Contains(head, "[Script Info]") || strings.Contains(head, "[V4+ Styles]") || strings.Contains(head, "[Events]"):
		return "ass"
	case strings.HasPrefix(strings.TrimSpace(head), "WEBVTT"):
		return "vtt"
	case h == "ass" || h == "ssa":
		return "ass"
	case h == "vtt":
		return "vtt"
	}
	return "srt"
}

// ---------------------------------------------------------------- SRT / VTT

var (
	srtTimeRe  = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})`)
	vttTimeRe  = regexp.MustCompile(`(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})\s*-->\s*(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})`)
	fontTagRe  = regexp.MustCompile(`(?is)</?font[^>]*>|</?u>|</?c[^>]*>|</?v[^>]*>|</?ruby>|</?rt>|\{\\[^}]*\}`)
	htmlTagRe  = regexp.MustCompile(`(?i)<(/?)(i|b)>`)
	lineEndRe  = regexp.MustCompile(`\r\n?`)
	multiNLRe  = regexp.MustCompile(`\n{2,}`)
	tsIdxRe    = regexp.MustCompile(`^\d+$`)
	assTimeRe  = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})[.:](\d{2})$`)
	assOvrRe   = regexp.MustCompile(`\{[^}]*\}`)
	assDrawRe  = regexp.MustCompile(`(?i)\\p[1-9]`)
	assHardNL  = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ")
	cueTextMax = 400
)

func stripVTTHeader(t string) string {
	t = lineEndRe.ReplaceAllString(t, "\n")
	if i := strings.Index(t, "\n\n"); i >= 0 && strings.HasPrefix(strings.TrimSpace(t), "WEBVTT") {
		return t[i+2:]
	}
	return t
}

func parseSRT(text string, vtt bool) []Cue {
	text = lineEndRe.ReplaceAllString(text, "\n")
	blocks := multiNLRe.Split(text, -1)
	var cues []Cue
	for _, b := range blocks {
		lines := strings.Split(strings.TrimSpace(b), "\n")
		if len(lines) < 2 {
			continue
		}
		ti := 0
		if tsIdxRe.MatchString(strings.TrimSpace(lines[0])) {
			ti = 1
		}
		if ti >= len(lines) {
			continue
		}
		var start, end int
		var ok bool
		if vtt {
			start, end, ok = vttTimes(lines[ti])
		}
		if !ok {
			start, end, ok = srtTimes(lines[ti])
		}
		if !ok {
			continue
		}
		txt := cleanText(strings.Join(lines[ti+1:], "\n"))
		if txt == "" || end <= start {
			continue
		}
		cues = append(cues, Cue{start, end, txt})
	}
	return normalize(cues)
}

func srtTimes(s string) (int, int, bool) {
	m := srtTimeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	return hms(m[1], m[2], m[3], m[4]), hms(m[5], m[6], m[7], m[8]), true
}

func vttTimes(s string) (int, int, bool) {
	m := vttTimeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	return hms(m[1], m[2], m[3], m[4]), hms(m[5], m[6], m[7], m[8]), true
}

func hms(h, m, s, ms string) int {
	for len(ms) < 3 {
		ms += "0"
	}
	return ((atoi(h)*60+atoi(m))*60+atoi(s))*1000 + atoi(ms)
}

func cleanText(t string) string {
	t = fontTagRe.ReplaceAllString(t, "")
	t = htmlTagRe.ReplaceAllString(t, "<$1$2>")
	t = strings.ReplaceAll(t, "&nbsp;", " ")
	var out []string
	for _, l := range strings.Split(t, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	s := strings.Join(out, "\n")
	if len(s) > cueTextMax {
		s = s[:cueTextMax]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// ---------------------------------------------------------------- ASS / SSA

func parseASS(text string) []Cue {
	text = lineEndRe.ReplaceAllString(text, "\n")
	inEvents := false
	var fields []string
	var cues []Cue
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "["):
			inEvents = strings.EqualFold(l, "[Events]")
			fields = nil
		case !inEvents:
		case strings.HasPrefix(l, "Format:"):
			fields = splitASS(strings.TrimPrefix(l, "Format:"), -1)
		case strings.HasPrefix(l, "Dialogue:"):
			if fields == nil {
				fields = []string{"Layer", "Start", "End", "Style", "Name", "MarginL", "MarginR", "MarginV", "Effect", "Text"}
			}
			vals := splitASS(strings.TrimPrefix(l, "Dialogue:"), len(fields))
			if len(vals) != len(fields) {
				continue
			}
			var start, end, txt = -1, -1, ""
			for i, f := range fields {
				switch strings.TrimSpace(f) {
				case "Start":
					start = assTime(vals[i])
				case "End":
					end = assTime(vals[i])
				case "Text":
					txt = vals[i]
				}
			}
			if start < 0 || end <= start || assDrawRe.MatchString(txt) {
				continue
			}
			txt = cleanText(assHardNL.Replace(assOvrRe.ReplaceAllString(txt, "")))
			if txt == "" {
				continue
			}
			cues = append(cues, Cue{start, end, txt})
		}
	}
	return normalize(cues)
}

func splitASS(s string, n int) []string {
	parts := strings.SplitN(s, ",", n)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func assTime(s string) int {
	m := assTimeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return -1
	}
	return hms(m[1], m[2], m[3], m[4]+"0")
}

// normalize sorts by start and merges cues with identical time spans (ASS bilingual subs are
// often two Dialogue lines, one per language, at the same time).
func normalize(cues []Cue) []Cue {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
	var out []Cue
	for _, c := range cues {
		if n := len(out); n > 0 && out[n-1].Start == c.Start && out[n-1].End == c.End {
			if !strings.Contains(out[n-1].Text, c.Text) {
				out[n-1].Text += "\n" + c.Text
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// RenderVTT writes cues as WebVTT.
func RenderVTT(cues []Cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		b.WriteString(vttTime(c.Start))
		b.WriteString(" --> ")
		b.WriteString(vttTime(c.End))
		b.WriteByte('\n')
		b.WriteString(strings.ReplaceAll(c.Text, "-->", "→"))
		b.WriteString("\n\n")
	}
	return b.String()
}

func vttTime(ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
