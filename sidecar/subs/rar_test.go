package subs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"hash/crc32"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwaples/rardecode/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// No free tool writes rar, so the archives are assembled here by hand, stored (uncompressed),
// after the published format notes, and checked against the library in TestRarBuilders.
type rarMem struct {
	name, data string
	enc        bool   // marked encrypted: unreadable without a password
	unknown    bool   // unpacked size not declared
	comp       uint64 // RAR5 compression information; 0 is stored
}

func rarPairs(pairs ...string) []rarMem {
	var ms []rarMem
	for i := 0; i+1 < len(pairs); i += 2 {
		ms = append(ms, rarMem{name: pairs[i], data: pairs[i+1]})
	}
	return ms
}

// rar5Block frames a RAR5 header: CRC32, size, body.
func rar5Block(body []byte) []byte {
	h := append(binary.AppendUvarint(nil, uint64(len(body))), body...)
	return append(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(h)), h...)
}

// makeRar5 writes a RAR5 archive; arcFlags 1 and endFlags 1 make it the first volume of several.
func makeRar5(arcFlags, endFlags uint64, ms ...rarMem) []byte {
	out := []byte("Rar!\x1a\x07\x01\x00")
	out = append(out, rar5Block(binary.AppendUvarint([]byte{1, 0}, arcFlags))...)
	for _, m := range ms {
		var extra []byte
		flags, fileFlags, size := uint64(2), uint64(4), uint64(len(m.data)) // data follows; CRC32 present
		if m.enc {
			rec := append([]byte{1, 0, 0, 15}, make([]byte, 32)...) // type, version, flags, KDF count, salt, IV
			extra = append(binary.AppendUvarint(nil, uint64(len(rec))), rec...)
			flags |= 1
		}
		if m.unknown {
			fileFlags, size = fileFlags|8, 0
		}
		b := binary.AppendUvarint([]byte{2}, flags)
		if m.enc {
			b = binary.AppendUvarint(b, uint64(len(extra)))
		}
		b = binary.AppendUvarint(b, uint64(len(m.data)))
		b = binary.AppendUvarint(b, fileFlags)
		b = binary.AppendUvarint(b, size)
		b = binary.AppendUvarint(b, 0x20) // attributes
		b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE([]byte(m.data)))
		b = append(binary.AppendUvarint(b, m.comp), 1) // compression information; host OS unix
		b = binary.AppendUvarint(b, uint64(len(m.name)))
		b = append(append(b, m.name...), extra...)
		out = append(append(out, rar5Block(b)...), m.data...)
	}
	return append(out, rar5Block(binary.AppendUvarint([]byte{5, 0}, endFlags))...)
}

// rar4Block frames a RAR 1.5-4.x header: CRC16 (the low half of the CRC32), type, flags, size, body.
func rar4Block(typ byte, flags uint16, body []byte) []byte {
	h := binary.LittleEndian.AppendUint16([]byte{typ}, flags)
	h = append(binary.LittleEndian.AppendUint16(h, uint16(7+len(body))), body...)
	return append(binary.LittleEndian.AppendUint16(nil, uint16(crc32.ChecksumIEEE(h))), h...)
}

// makeRar4 writes a RAR 4.x archive; names are stored as given (no Unicode flag).
func makeRar4(ms ...rarMem) []byte {
	out := append([]byte("Rar!\x1a\x07\x00"), rar4Block(0x73, 0, make([]byte, 6))...)
	for _, m := range ms {
		flags, size := uint16(0x8000), uint32(len(m.data))
		if m.enc {
			flags |= 4
		}
		if m.unknown {
			size = 0xffffffff
		}
		b := binary.LittleEndian.AppendUint32(nil, uint32(len(m.data)))
		b = binary.LittleEndian.AppendUint32(b, size)
		b = append(b, 2) // host OS win32
		b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE([]byte(m.data)))
		b = binary.LittleEndian.AppendUint32(b, 0x5a8e3b20) // DOS time
		b = append(b, 29, 0x30)                             // unpack version; stored
		b = binary.LittleEndian.AppendUint16(b, uint16(len(m.name)))
		b = binary.LittleEndian.AppendUint32(b, 0x20)
		out = append(append(out, rar4Block(0x74, flags, append(b, m.name...))...), m.data...)
	}
	return append(out, rar4Block(0x7b, 0x4000, nil)...)
}

// rarKinds runs f with both writers.
func rarKinds(t *testing.T, f func(t *testing.T, mk func(...rarMem) []byte)) {
	t.Run("rar5", func(t *testing.T) { f(t, func(ms ...rarMem) []byte { return makeRar5(0, 0, ms...) }) })
	t.Run("rar4", func(t *testing.T) { f(t, makeRar4) })
}

// The hand-made archives are what the library itself reads, checksums included.
func TestRarBuilders(t *testing.T) {
	rarKinds(t, func(t *testing.T, mk func(...rarMem) []byte) {
		rr, err := rardecode.NewReader(bytes.NewReader(mk(rarPairs("dir/a.srt", srtUTF8, "b.txt", "", "c.srt", srtEng)...)))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range rarPairs("dir/a.srt", srtUTF8, "b.txt", "", "c.srt", srtEng) {
			h, err := rr.Next()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rr)
			if h.Name != want.name || string(b) != want.data || err != nil || h.UnPackedSize != int64(len(want.data)) || h.IsDir || h.Encrypted {
				t.Fatalf("member %+v: %d bytes, err=%v, want %s", h, len(b), err, want.name)
			}
		}
		if _, err := rr.Next(); err != io.EOF {
			t.Fatalf("after the last member: %v", err)
		}
	})
}

const srtE2 = "1\n00:00:01,000 --> 00:00:02,000\n第二集\n"

var updateFixtures = flag.Bool("update", false, "rewrite testdata/pack.rar")

// packRar is testdata/pack.rar: a season pack whose members are not named like ASSRT's file list.
func packRar() []byte {
	return makeRar5(0, 0, rarPairs("pack/Show.S01E01.chs.srt", srtUTF8, "pack/Show.S01E01.eng.srt", srtEng,
		"pack/Show.S01E02.chs.srt", srtE2, "pack/readme.txt", "nope", "字幕/塔尔萨之王.S03E01.简体.srt", srtUTF8)...)
}

func TestRarFixtures(t *testing.T) {
	if *updateFixtures {
		if err := os.WriteFile("testdata/pack.rar", packRar(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := os.ReadFile("testdata/pack.rar")
	if err != nil || !bytes.Equal(pack, packRar()) {
		t.Fatalf("testdata/pack.rar is not what packRar writes (err=%v); go test ./subs -run TestRarFixtures -update", err)
	}
	// sample.rar was written by WinRAR (RAR5, 32 MB dictionary): directories, one compressed
	// member, which has to come out whole and with a good checksum, and two symbolic
	// links, which have no contents of their own: those are unreadable.
	sample, err := os.ReadFile("testdata/sample.rar")
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	err = rarWalk(context.Background(), sample, func(h *rardecode.FileHeader, r io.Reader) bool {
		b, err := io.ReadAll(r)
		if link := h.LinkType != rardecode.LinkTypeNone; link != (err != nil) || link != (rarRead(h, r) == nil) {
			t.Fatalf("%s: link type %d, err=%v", h.Name, h.LinkType, err)
		}
		sizes[h.Name] = len(b)
		return true
	})
	if err != nil || len(sizes) != 7 || sizes["testdata/proverb3.txt"] != 0 || sizes["testdata/already-compressed.jpg"] != 8944 || sizes["testdata/proverbs/proverb1.txt"] != 54 {
		t.Fatalf("sample.rar: err=%v sizes=%v", err, sizes)
	}
	if es := Expand("sample.rar", sample); len(es) != 0 {
		t.Fatalf("sample.rar has no subtitle, expanded %v", names(es))
	}
}

func TestExpandRar(t *testing.T) {
	es := Expand("pack.rar", packRar())
	if got := strings.Join(names(es), " "); got != "Show.S01E01.chs.srt Show.S01E01.eng.srt Show.S01E02.chs.srt 塔尔萨之王.S03E01.简体.srt" {
		t.Fatalf("expanded %q", got)
	}
	if string(es[0].Data) != srtUTF8 || string(es[1].Data) != srtEng || string(es[2].Data) != srtE2 {
		t.Fatalf("member bytes differ: %q", es[2].Data)
	}
	if best := Pick(es, "Show.S01E02.1080p.WEB-DL.mkv"); best == nil || best.Name != "Show.S01E02.chs.srt" {
		t.Fatalf("picked %v", best)
	}
	// RAR 4: backslash directories, a GBK name without the Unicode flag, junk skipped
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String("字幕\\塔尔萨之王.S03E01.简体.srt")
	es = Expand("pack.rar", makeRar4(rarPairs(`pack\a.chs.srt`, srtUTF8, gbk, srtEng, "readme.txt", "nope",
		`__MACOSX\._a.chs.srt`, "junk", ".hidden.srt", "junk", "empty.srt", "")...))
	if got := strings.Join(names(es), " "); got != "a.chs.srt 塔尔萨之王.S03E01.简体.srt" || string(es[1].Data) != srtEng {
		t.Fatalf("rar4 expanded %q", got)
	}
}

// ASSRT's per-file link fails and the package is a rar: the member is taken out of it.
func TestASSRTFallbackRar(t *testing.T) {
	name := "塔尔萨之王.S03E01.简体.srt"
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String(name)
	for _, c := range []struct {
		title, want string
		body        []byte
	}{
		{"rar5", "Show.S01E01.eng.srt", packRar()},
		{"rar5 chinese name", name, packRar()},
		{"rar5 folded name", "show.s01e02.CHS.srt", packRar()},
		{"rar5 exact before folded", "x.srt", makeRar5(0, 0, rarPairs("X.SRT", srtE2, "x.srt", srtEng)...)},
		{"rar4", "x.eng.srt", makeRar4(rarPairs(`pack\x.chs.srt`, srtUTF8, `pack\x.eng.srt`, srtEng)...)},
		{"rar4 gbk name", name, makeRar4(rarPairs(`pack\x.chs.srt`, srtUTF8, "pack\\"+gbk, srtEng)...)},
	} {
		t.Run(c.title, func(t *testing.T) {
			srv, hits := fallbackCDN(t, 493, serveBytes(c.body))
			data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, c.want, "pack.rar"))
			want := srtEng
			if strings.Contains(c.title, "folded") && !strings.Contains(c.title, "exact") {
				want = srtE2
			} else if c.title == "rar5 chinese name" {
				want = srtUTF8
			}
			if err != nil || string(data) != want {
				t.Fatalf("download err=%v data=%q", err, data)
			}
			if *hits != 1 {
				t.Fatalf("archive hits %d", *hits)
			}
		})
	}
}

// No member is named like the file list says: the whole rar is handed on for Expand and Pick.
func TestASSRTFallbackRarNoNameMatch(t *testing.T) {
	srv, _ := fallbackCDN(t, 493, serveBytes(packRar()))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.rar"))
	if err != nil || !bytes.Equal(data, packRar()) {
		t.Fatalf("download err=%v len=%d, want the %d rar bytes", err, len(data), len(packRar()))
	}
	if es := Expand("x.chs.srt", data); len(es) != 4 {
		t.Fatalf("expanded %v", names(es))
	}
}

// A rar from the network is bounded like a zip: members scanned, bytes per member, bytes kept.
func TestRarLimits(t *testing.T) {
	oldEntries, oldMember, oldTotal := maxZipEntries, maxZipMember, maxZipTotal
	t.Cleanup(func() { maxZipEntries, maxZipMember, maxZipTotal = oldEntries, oldMember, oldTotal })
	cue := strings.Repeat("x", 50)
	rarKinds(t, func(t *testing.T, mk func(...rarMem) []byte) {
		t.Run("entries", func(t *testing.T) {
			maxZipEntries, maxZipMember, maxZipTotal = 3, oldMember, oldTotal
			z := mk(rarPairs("a.srt", cue, "b.srt", cue, "c.srt", cue, "d.srt", cue, "e.srt", cue)...)
			if got := strings.Join(names(Expand("pack.rar", z)), " "); got != "a.srt b.srt c.srt" {
				t.Fatalf("expanded %q", got)
			}
			if hit, n, err := rarMember(context.Background(), z, "e.srt"); hit != nil || n != 3 || err != nil {
				t.Fatalf("rarMember past the cap: hit=%v subtitles=%d err=%v", hit, n, err)
			}
		})
		t.Run("member", func(t *testing.T) {
			maxZipEntries, maxZipMember, maxZipTotal = oldEntries, 100, oldTotal
			// lying.srt does not declare its size and carries 300 bytes: dropped, not cut to the limit
			z := mk(rarMem{name: "big.srt", data: strings.Repeat("y", 300)}, rarMem{name: "lying.srt", data: strings.Repeat("z", 300), unknown: true},
				rarMem{name: "small.srt", data: cue}, rarMem{name: "honest.srt", data: cue, unknown: true})
			es := Expand("pack.rar", z)
			if len(es) != 2 || es[0].Name != "small.srt" || string(es[0].Data) != cue || es[1].Name != "honest.srt" || string(es[1].Data) != cue {
				t.Fatalf("expanded %v", names(es))
			}
			for _, n := range []string{"big.srt", "lying.srt"} {
				hit, subtitles, err := rarMember(context.Background(), z, n)
				if hit != nil {
					t.Fatalf("rarMember returned %d bytes of oversize %s", len(hit.Data), n)
				}
				var um *unreadableMember
				if !errors.As(err, &um) || um.Name != n {
					t.Fatalf("rarMember oversize %s: subtitles=%d err=%v", n, subtitles, err)
				}
			}
			if hit, n, err := rarMember(context.Background(), z, "absent.srt"); hit != nil || n != 3 || err != nil {
				t.Fatalf("rarMember miss: hit=%v subtitles=%d err=%v", hit, n, err)
			}
			if hit, _, err := rarMember(context.Background(), z, "SMALL.srt"); err != nil || hit == nil || hit.Name != "small.srt" || string(hit.Data) != cue {
				t.Fatalf("rarMember small: hit=%v err=%v", hit, err)
			}
		})
		t.Run("total", func(t *testing.T) {
			maxZipEntries, maxZipMember, maxZipTotal = oldEntries, oldMember, 120
			es := Expand("pack.rar", mk(rarPairs("a.srt", cue, "b.srt", cue, "c.srt", cue, "d.srt", cue)...))
			if got := strings.Join(names(es), " "); got != "a.srt b.srt" {
				t.Fatalf("kept %q", got)
			}
		})
		t.Run("no subtitle", func(t *testing.T) {
			maxZipEntries, maxZipMember, maxZipTotal = oldEntries, oldMember, oldTotal
			srv, _ := fallbackCDN(t, 493, serveBytes(mk(rarPairs("readme.txt", "nope", "__MACOSX/._x.chs.srt", "junk")...)))
			_, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.rar"))
			if want := "assrt: per-file download failed (HTTP 493) and the archive pack.rar has no subtitle file"; err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	})
}

// Archives that cannot be read give the same short errors as zips, never a panic or a hang.
func TestRarUnreadable(t *testing.T) {
	const pre = "assrt: per-file download failed (HTTP 493) and "
	download := func(t *testing.T, body []byte, name string) ([]byte, error) {
		t.Helper()
		srv, _ := fallbackCDN(t, 493, serveBytes(body))
		return newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, name, "pack.rar"))
	}
	pack := packRar()
	encHeaders := append([]byte("Rar!\x1a\x07\x01\x00"), rar5Block(append([]byte{4, 0, 0, 0, 15}, make([]byte, 16)...))...)
	for title, body := range map[string][]byte{
		"magic only":        []byte("Rar!\x1a\x07\x00"),
		"zeroes":            []byte("Rar!\x1a\x07\x00" + strings.Repeat("\x00", 64)),
		"truncated header":  pack[:len(pack)-70],
		"corrupt header":    append(append([]byte{}, pack[:20]...), bytes.Repeat([]byte{0xff}, 200)...),
		"encrypted headers": encHeaders,
		"first volume":      makeRar5(1, 1, rarPairs("y.chs.srt", srtUTF8)...),
	} {
		t.Run(title, func(t *testing.T) {
			_, err := download(t, body, "x.chs.srt")
			if want := pre + "the archive pack.rar could not be read"; err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
			Expand("pack.rar", body) // must not panic
		})
	}
	rarKinds(t, func(t *testing.T, mk func(...rarMem) []byte) {
		z := mk(rarMem{name: "x.chs.srt", data: srtUTF8, enc: true}, rarMem{name: "x.eng.srt", data: srtEng})
		// the wanted member is encrypted: say so rather than pick another
		_, err := download(t, z, "x.chs.srt")
		if want := pre + "x.chs.srt in the archive could not be read"; err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
		if data, err := download(t, z, "x.eng.srt"); err != nil || string(data) != srtEng {
			t.Fatalf("readable member next to an encrypted one: err=%v data=%q", err, data)
		}
		if got := strings.Join(names(Expand("pack.rar", z)), " "); got != "x.eng.srt" {
			t.Fatalf("expanded %q", got)
		}
		// contents that do not match their checksum are not handed on
		bad := bytes.Replace(z, []byte("english only"), []byte("english 0nly"), 1)
		if _, err := download(t, bad, "x.eng.srt"); err == nil || err.Error() != pre+"x.eng.srt in the archive could not be read" {
			t.Fatalf("bad checksum: %v", err)
		}
		if es := Expand("pack.rar", bad); len(es) != 0 {
			t.Fatalf("bad checksum expanded %v", names(es))
		}
		// a member cut short is unreadable, the ones before it are not
		cut := mk(rarPairs("a.srt", srtEng, "x.chs.srt", srtUTF8)...)
		cut = cut[:bytes.LastIndex(cut, []byte(srtUTF8))+10]
		if _, err := download(t, cut, "x.chs.srt"); err == nil || err.Error() != pre+"x.chs.srt in the archive could not be read" {
			t.Fatalf("cut member: %v", err)
		}
		if got := strings.Join(names(Expand("pack.rar", cut)), " "); got != "a.srt" {
			t.Fatalf("cut archive expanded %q", got)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		old := rarTimeout
		rarTimeout = -time.Second
		t.Cleanup(func() { rarTimeout = old })
		_, err := download(t, pack, "Show.S01E01.eng.srt")
		if want := pre + "the archive pack.rar could not be read"; err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
		if es := Expand("pack.rar", pack); len(es) != 1 || es[0].Name != "pack.rar" { // not expanded: left to the parser, like a broken zip
			t.Fatalf("expanded %v", names(es))
		}
	})
	t.Run("dictionary", func(t *testing.T) {
		old := maxRarDict
		maxRarDict = 1 << 10 // below the library's minimum window: no compressed member can be opened
		t.Cleanup(func() { maxRarDict = old })
		sample, _ := os.ReadFile("testdata/sample.rar")
		if err := rarWalk(context.Background(), sample, func(*rardecode.FileHeader, io.Reader) bool { return true }); err == nil {
			t.Fatal("a member needing a larger window than allowed was opened")
		}
	})
	// damaged archives of both kinds, stored and compressed: any result, but no panic and no hang
	t.Run("damaged", func(t *testing.T) {
		sample, _ := os.ReadFile("testdata/sample.rar")
		for _, z := range [][]byte{pack, makeRar4(rarPairs(`pack\x.chs.srt`, srtUTF8, "x.eng.srt", srtEng)...), sample} {
			step := 1 + len(z)/600
			for i := 0; i < len(z); i += step {
				flipped := append([]byte{}, z...)
				flipped[i] ^= 0x5a
				for _, d := range [][]byte{z[:i], flipped} {
					Expand("pack.rar", d)
					rarMember(context.Background(), d, "x.eng.srt")
					rarWalk(context.Background(), d, func(_ *rardecode.FileHeader, r io.Reader) bool {
						io.Copy(io.Discard, io.LimitReader(r, 1<<20))
						return true
					})
				}
			}
		}
	})
}

// rar4Member is one file of a RAR 4.x archive as the RAR 3 packer would write it: method 0x30 is
// stored, 0x33 packed. large writes the 64-bit form of the sizes.
func rar4Member(name string, method byte, unp int64, large bool, packed []byte) []byte {
	flags := uint16(0x8000)
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(packed)))
	b = binary.LittleEndian.AppendUint32(b, uint32(unp))
	b = append(b, 2)
	b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(packed))
	b = binary.LittleEndian.AppendUint32(b, 0x5a8e3b20)
	b = append(b, 29, method)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(name)))
	b = binary.LittleEndian.AppendUint32(b, 0x20)
	if large {
		flags |= 0x100
		b = binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(b, 0), uint32(unp>>32))
	}
	return append(rar4Block(0x74, flags, append(b, name...)), packed...)
}

// rar4Arc puts members between the RAR 4.x signature, archive header and end block.
func rar4Arc(members ...[]byte) []byte {
	out := append([]byte("Rar!\x1a\x07\x00"), rar4Block(0x73, 0, make([]byte, 6))...)
	return append(append(out, bytes.Join(members, nil)...), rar4Block(0x7b, 0x4000, nil)...)
}

// ppmdMax opens a RAR 3 packed stream with a PPMd block that asks for the largest model, 256 MB.
var ppmdMax = append([]byte{0x80 | 0x20 | 2, 0xff}, make([]byte, 30)...)

// allocated is what f allocated, in bytes.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readAllRar(z []byte) (n int, err error) {
	err = rarWalk(context.Background(), z, func(_ *rardecode.FileHeader, r io.Reader) bool {
		io.Copy(io.Discard, io.LimitReader(r, 1<<20))
		n++
		return true
	})
	return n, err
}

// RAR 5 is read whole; of RAR 1.5 to 4.x only stored members: its packed members may select the
// PPMd coder, whose model the archive sizes (up to 256 MB, several times that in this decoder).
func TestRarOldFormat(t *testing.T) {
	const pre = "assrt: per-file download failed (HTTP 493) and "
	const old = pre + "the archive is an old-format rar (RAR4, compressed), which is not supported: pack.rar"
	download := func(t *testing.T, body []byte, name string) ([]byte, error) {
		t.Helper()
		srv, _ := fallbackCDN(t, 493, serveBytes(body))
		return newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, name, "pack.rar"))
	}
	// nothing of the archive is decoded: the walk fails at the member and allocates next to nothing
	refused := func(t *testing.T, z []byte) {
		t.Helper()
		var n int
		var err error
		got := allocated(func() { n, err = readAllRar(z) })
		if err != errRarOld || n != 0 || got > 8<<20 {
			t.Fatalf("walk: %d members read, err=%v, %d KB allocated", n, err, got>>10)
		}
		t.Logf("walk allocated %d KB", got>>10)
		var es []Entry
		if got := allocated(func() { es = Expand("pack.rar", z) }); len(es) != 1 || es[0].Name != "pack.rar" || got > 8<<20 {
			t.Fatalf("expanded %v, %d KB allocated", names(es), got>>10)
		}
		if _, err := download(t, z, "x.srt"); err == nil || err.Error() != old {
			t.Fatalf("got %v, want %q", err, old)
		}
	}
	t.Run("ppmd largest model", func(t *testing.T) {
		refused(t, rar4Arc(rar4Member("x.srt", 0x33, 4096, false, ppmdMax)))
	})
	// sizes the window check of the library does not catch
	for _, unp := range []int64{-2, -1 << 63, -1 << 32} {
		t.Run("ppmd negative size", func(t *testing.T) {
			z := rar4Arc(rar4Member("x.srt", 0x33, unp, true, ppmdMax))
			var n int
			var err error
			if got := allocated(func() { n, err = readAllRar(z); Expand("pack.rar", z); rarMember(context.Background(), z, "x.srt") }); err == nil || n != 0 || got > 8<<20 {
				t.Fatalf("size %d: %d members read, err=%v, %d KB allocated", unp, n, err, got>>10)
			}
		})
	}
	t.Run("ppmd size unknown", func(t *testing.T) {
		refused(t, rar4Arc(rar4Member("x.srt", 0x33, -1, false, ppmdMax)))
	})
	t.Run("ppmd by rar", func(t *testing.T) { refused(t, fixture(t, "rar4_ppmd.rar")) })
	t.Run("lz by rar", func(t *testing.T) { refused(t, fixture(t, "rar4_lz.rar")) })
	t.Run("stored by rar", func(t *testing.T) {
		z := fixture(t, "rar4_stored.rar")
		es := Expand("pack.rar", z)
		if got := strings.Join(names(es), " "); got != "Show.S01E02.chs.srt Show.S01E01.chs.srt Show.S01E03.chs.srt" || !strings.Contains(string(es[2].Data), "episode 3 episode 3") {
			t.Fatalf("expanded %q", got)
		}
		if data, err := download(t, z, "Show.S01E03.chs.srt"); err != nil || !bytes.Equal(data, es[2].Data) {
			t.Fatalf("download err=%v data=%q", err, data)
		}
	})
	// stored members in front of a packed one are read; the packed one and what follows it are not
	t.Run("stored then packed", func(t *testing.T) {
		z := rar4Arc(rar4Member("a.srt", 0x30, int64(len(srtEng)), false, []byte(srtEng)), rar4Member("x.srt", 0x33, 4096, false, ppmdMax),
			rar4Member("c.srt", 0x30, int64(len(srtE2)), false, []byte(srtE2)))
		if data, err := download(t, z, "a.srt"); err != nil || string(data) != srtEng {
			t.Fatalf("stored member: err=%v data=%q", err, data)
		}
		for _, n := range []string{"x.srt", "c.srt", "absent.srt"} {
			if _, err := download(t, z, n); err == nil || err.Error() != old {
				t.Fatalf("%s: got %v, want %q", n, err, old)
			}
		}
		if got := strings.Join(names(Expand("pack.rar", z)), " "); got != "a.srt" {
			t.Fatalf("expanded %q", got)
		}
	})
	// only a signature at the very start counts: the library would look for one further on
	t.Run("unknown version", func(t *testing.T) {
		for _, sig := range []string{"Rar!\x1a\x07\x01\x01junk", "Rar!\x1a\x07\x02\x00", "Rar!\x1a\x07\x01", "Rar!\x1a\x07"} {
			z := append([]byte(sig), packRar()...)
			if n, err := readAllRar(z); err == nil || err.Error() != "rar: unknown version" || n != 0 {
				t.Fatalf("%q: %d members read, err=%v", sig, n, err)
			}
			if es := Expand("pack.rar", z); len(es) != 1 || es[0].Name != "pack.rar" {
				t.Fatalf("%q: expanded %v", sig, names(es))
			}
			if _, err := download(t, z, "Show.S01E01.eng.srt"); err == nil || err.Error() != pre+"the archive pack.rar could not be read" {
				t.Fatalf("%q: %v", sig, err)
			}
		}
	})
	t.Run("rar5 packed", func(t *testing.T) {
		sizes := map[string]int{}
		err := rarWalk(context.Background(), fixture(t, "sample.rar"), func(h *rardecode.FileHeader, r io.Reader) bool {
			sizes[h.Name] = len(rarRead(h, r))
			return true
		})
		if err != nil || sizes["testdata/already-compressed.jpg"] != 8944 || sizes["testdata/proverbs/proverb1.txt"] != 54 {
			t.Fatalf("sample.rar: err=%v sizes=%v", err, sizes)
		}
	})
}

// A RAR 4 archive that breaks off is not taken for a shorter one: the library reports running out
// of data between two blocks, or while skipping a member, as the end of the archive.
func TestRarCutShort(t *testing.T) {
	const want = "assrt: per-file download failed (HTTP 493) and the archive pack.rar could not be read"
	z := fixture(t, "rar4_stored.rar")
	second := bytes.Index(z, []byte("Show.S01E01.chs.srt"))
	third := bytes.Index(z, []byte("Show.S01E03.chs.srt")) - 32 // where the third member's header starts
	noEnd := makeRar4(rarPairs("a.srt", srtEng, "b.srt", srtE2)...)
	for title, cut := range map[string][]byte{
		"inside a member":         z[:second+60],
		"between members":         z[:third],
		"before the end block":    z[:len(z)-7],
		"hand-made, no end block": noEnd[:len(noEnd)-7],
	} {
		t.Run(title, func(t *testing.T) {
			if err := rarWalk(context.Background(), cut, func(*rardecode.FileHeader, io.Reader) bool { return true }); err == nil {
				t.Fatal("walked to the end of an archive without one")
			}
			// a member that is not in front of the break is not there: no other one is picked for it
			srv, _ := fallbackCDN(t, 493, serveBytes(cut))
			for _, n := range []string{"Show.S01E03.chs.srt", "Show.S01E04.chs.srt"} {
				if n == "Show.S01E03.chs.srt" && strings.Contains(title, "end block") {
					continue // whole, only the end block is missing
				}
				data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, n, "pack.rar"))
				if err == nil || err.Error() != want {
					t.Fatalf("%s: got %d bytes, err=%v; want %q", n, len(data), err, want)
				}
			}
			// a member in front of the break is still found by name
			first := "Show.S01E02.chs.srt"
			if strings.HasPrefix(title, "hand-made") {
				first = "a.srt"
			}
			if hit, _, err := rarMember(context.Background(), cut, first); err != nil || hit == nil || hit.Name != first {
				t.Fatalf("%s: hit=%v err=%v", first, hit, err)
			}
		})
	}
	if err := rarWalk(context.Background(), z, func(*rardecode.FileHeader, io.Reader) bool { return true }); err != nil {
		t.Fatalf("whole archive: %v", err)
	}
	// bytes after the end block do not matter
	if es := Expand("pack.rar", append(append([]byte{}, z...), "trailing"...)); len(es) != 3 {
		t.Fatalf("trailing bytes: expanded %v", names(es))
	}
}

// The window a RAR 5 member may ask for: 32 MB, WinRAR 5's default, and no more.
func TestRarWindowCap(t *testing.T) {
	if maxRarDict != 32<<20 {
		t.Fatalf("maxRarDict is %d MB", maxRarDict>>20)
	}
	for mb, ok := range map[uint64]bool{32: true, 64: false} {
		bits := uint64(8) // 128 KB << 8 is 32 MB
		if mb == 64 {
			bits = 9
		}
		opened := 0
		err := rarWalk(context.Background(), makeRar5(0, 0, rarMem{name: "x.srt", data: "xxxx", unknown: true, comp: 1<<7 | bits<<10}), func(*rardecode.FileHeader, io.Reader) bool {
			opened++
			return true
		})
		if (opened == 1) != ok || (err == nil) != ok {
			t.Fatalf("%d MB window: opened=%d err=%v", mb, opened, err)
		}
	}
}

// holdRar takes the place of a rar that is being decoded until the returned function is called.
func holdRar() (release func()) {
	rarSem <- struct{}{}
	return func() { <-rarSem }
}

// Waiting for another rar to be done ends with the request.
func TestRarWaitEndsWithRequest(t *testing.T) {
	release := holdRar()
	var once bool
	defer func() {
		if !once {
			release()
		}
	}()
	srv, _ := fallbackCDN(t, 493, serveBytes(packRar()))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := newTestASSRT(srv, "tok").Download(ctx, fallbackFile(srv.URL, "Show.S01E01.eng.srt", "pack.rar"))
		done <- err
	}()
	select {
	case err := <-done:
		if want := "assrt: per-file download failed (HTTP 493) and the archive pack.rar could not be read in time"; err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	case <-time.After(3 * time.Second):
		release()
		once = true
		t.Fatalf("still waiting for the other rar 2.7 s after the request ended (then: %v)", <-done)
	}
	release()
	once = true
	if data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "Show.S01E01.eng.srt", "pack.rar")); err != nil || string(data) != srtEng {
		t.Fatalf("after the other rar was done: err=%v data=%q", err, data)
	}
}
