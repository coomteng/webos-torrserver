package subs

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func enc(t *testing.T, s string, e encoding.Encoding) []byte {
	t.Helper()
	b, _, err := transform.Bytes(e.NewEncoder(), []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const srtUTF8 = "1\r\n00:00:01,000 --> 00:00:03,500\r\n<font color=\"#ffff00\">你好，世界</font>\r\nHello world\r\n\r\n2\r\n00:01:02,250 --> 00:01:04,000\r\n<i>第二句</i>\r\n\r\n\r\n3\r\n00:01:05,000 --> 00:01:05,000\r\nzero length, dropped\r\n"

func TestSRTToVTT(t *testing.T) {
	vtt, format, err := ToVTT([]byte(srtUTF8), ".srt")
	if err != nil || format != "srt" {
		t.Fatalf("err=%v format=%s", err, format)
	}
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:03.500\n你好，世界\nHello world\n\n00:01:02.250 --> 00:01:04.000\n<i>第二句</i>\n\n"
	if vtt != want {
		t.Fatalf("got:\n%q\nwant:\n%q", vtt, want)
	}
}

func TestGBKAndUTF16(t *testing.T) {
	gbk := enc(t, srtUTF8, simplifiedchinese.GBK)
	if bytes.Equal(gbk, []byte(srtUTF8)) {
		t.Fatal("fixture is not actually GBK")
	}
	vtt, _, err := ToVTT(gbk, "srt")
	if err != nil || !strings.Contains(vtt, "你好，世界") {
		t.Fatalf("GBK: err=%v vtt=%q", err, vtt)
	}
	u16 := append([]byte{0xFF, 0xFE}, enc(t, srtUTF8, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM))...)
	vtt, _, err = ToVTT(u16, "")
	if err != nil || !strings.Contains(vtt, "第二句") {
		t.Fatalf("UTF-16LE: err=%v vtt=%q", err, vtt)
	}
	bom := append([]byte{0xEF, 0xBB, 0xBF}, srtUTF8...)
	if vtt, _, _ = ToVTT(bom, ""); !strings.HasPrefix(vtt, "WEBVTT") {
		t.Fatalf("UTF-8 BOM not stripped: %q", vtt[:20])
	}
}

const assBilingual = `[Script Info]
Title: test
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname
Style: Default,Arial

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\an8\pos(100,200)}你好，世界
Dialogue: 0,0:00:01.00,0:00:03.50,Eng,,0,0,0,,Hello world
Dialogue: 0,0:00:05.20,0:00:06.00,Default,,0,0,0,,第一行\N第二行, 带逗号
Dialogue: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,{\p1}m 0 0 l 100 0 100 100{\p0}
`

func TestASSToVTT(t *testing.T) {
	vtt, format, err := ToVTT([]byte(assBilingual), "ass")
	if err != nil || format != "ass" {
		t.Fatalf("err=%v format=%s", err, format)
	}
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:03.500\n你好，世界\nHello world\n\n00:00:05.200 --> 00:00:06.000\n第一行\n第二行, 带逗号\n\n"
	if vtt != want {
		t.Fatalf("got:\n%q\nwant:\n%q", vtt, want)
	}
}

func TestVTTPassThrough(t *testing.T) {
	in := "WEBVTT\nKind: captions\n\n00:01.000 --> 00:02.000 align:start\n<c.yellow>hi</c>\n\n1\n01:00:01.000 --> 01:00:02.000\nlate\n"
	vtt, format, err := ToVTT([]byte(in), "")
	if err != nil || format != "vtt" {
		t.Fatalf("err=%v format=%s", err, format)
	}
	if !strings.Contains(vtt, "00:00:01.000 --> 00:00:02.000\nhi\n") || !strings.Contains(vtt, "01:00:01.000 --> 01:00:02.000\nlate") {
		t.Fatalf("got %q", vtt)
	}
}

func TestGarbage(t *testing.T) {
	if _, _, err := ToVTT([]byte("<html>not a subtitle</html>"), "srt"); err == nil {
		t.Fatal("expected error for garbage")
	}
}

func TestSearchQuery(t *testing.T) {
	cases := map[string]string{
		"沙丘2.Dune.Part.Two.2024.1080p.AMZN.WEB-DL.DDP5.1.Atmos.H.264-FLUX.mkv": "Dune Part Two 2024",
		"Shogun.S01E03.1080p.WEB.h264-ETHEL.mkv":                               "Shogun S01E03",
		"Season 1/The.Bear.S02E05.720p.HDTV.x264.mp4":                          "The Bear S02E05",
		"[Group] Frieren - 12 [1080p].mkv":                                     "Frieren 12",
		"Big_Buck_Bunny_1080p_surround.avi":                                    "Big Buck Bunny",
		"庆余年2.第05集.4K.mp4":                                                     "庆余年2 E05",
		"Oppenheimer (2023) [2160p] [HDR].mkv":                                 "Oppenheimer 2023",
		"audiotest.mkv":                                                        "audiotest",
	}
	for in, want := range cases {
		if got := SearchQuery(in); got != want {
			t.Errorf("SearchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	for in, want := range map[string]Episode{"a.S01E03.b": {1, 3}, "x 2x10": {2, 10}, "第12集": {0, 12}, "Frieren E07": {0, 7}} {
		if got, ok := ParseEpisode(in); !ok || got != want {
			t.Errorf("ParseEpisode(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseEpisode("Dune.Part.Two.2024.1080p"); ok {
		t.Error("1080p must not parse as an episode")
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

func TestExpandAndPick(t *testing.T) {
	z := makeZip(t, map[string]string{
		"__MACOSX/._x.srt":             "junk",
		"readme.txt":                   "nope",
		"pack/Show.S01E01.chs.srt":     srtUTF8,
		"pack/Show.S01E02.chs&eng.srt": srtUTF8,
		"pack/Show.S01E02.eng.srt":     srtUTF8,
		"pack/Show.S01E02.chs.ass":     assBilingual,
	})
	es := Expand("pack.zip", z)
	if len(es) != 4 {
		t.Fatalf("expanded %d entries: %+v", len(es), names(es))
	}
	best := Pick(es, "Show.S01E02.1080p.WEB-DL.mkv")
	if best == nil || best.Name != "Show.S01E02.chs.ass" && best.Name != "Show.S01E02.chs&eng.srt" {
		t.Fatalf("picked %v", best)
	}
	// chs (100) beats dou (80); ass vs srt: chs.ass=100, chs&eng.srt=chs+dou+srt=185 -> prefer the bilingual srt
	if best.Name != "Show.S01E02.chs&eng.srt" {
		t.Fatalf("picked %s, want the bilingual srt", best.Name)
	}
	if LangLabel(LangOf(best.Name)) != "简中·英文·双语" {
		t.Fatalf("label %q", LangLabel(LangOf(best.Name)))
	}
	if e := Expand("x.srt", []byte(srtUTF8)); len(e) != 1 || e[0].Name != "x.srt" {
		t.Fatalf("plain file: %+v", names(e))
	}
}

func names(es []Entry) []string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name)
	}
	return n
}
