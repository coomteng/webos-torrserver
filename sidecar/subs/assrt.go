package subs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Result is one subtitle package offered by a source.
type Result struct {
	Src    string   `json:"src"`             // "assrt"
	ID     string   `json:"id"`              // source-specific
	Title  string   `json:"title"`           // native name / release name shown to the user
	Lang   []string `json:"lang"`            // tags: chs cht eng dou jap kor
	Label  string   `json:"label"`           // 简中·英文·双语
	Format string   `json:"format"`          // srt / ass / ""
	Files  []string `json:"files,omitempty"` // file names inside the package, if known
	Score  int      `json:"score"`           // votes / downloads, for ordering
	Time   string   `json:"time,omitempty"`  // upload time as given by the source
}

// File is one downloadable subtitle file belonging to a Result.
type File struct {
	Name string
	URL  string
	// The whole package the file belongs to, for when the per-file link fails. Empty if unknown.
	Archive     string
	ArchiveName string
}

// Source is a subtitle site.
type Source interface {
	Name() string
	Search(ctx context.Context, q string) ([]Result, error)
	Files(ctx context.Context, id string) ([]File, error)
	Download(ctx context.Context, f File) ([]byte, error)
}

// ErrNotConfigured is returned by a source without credentials.
var ErrNotConfigured = errors.New("subtitle source not configured")

// ---------------------------------------------------------------- ASSRT (assrt.net / 射手网伪)

// ASSRT talks to https://api.assrt.net/v1. The free quota is tiny (4–5 requests/min, keyed by token
// and by IP), so calls go through a one-at-a-time gate with a fixed spacing, and searches are cached.
type ASSRT struct {
	Token   string
	Base    string // default https://api.assrt.net/v1
	Spacing time.Duration
	Client  *http.Client
	UA      string

	mu    sync.Mutex
	last  time.Time
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at  time.Time
	res []Result
}

const assrtCacheTTL = 10 * time.Minute

func NewASSRT(token string) *ASSRT {
	return &ASSRT{Token: token, Base: "https://api.assrt.net/v1", Spacing: 15 * time.Second,
		Client: &http.Client{Timeout: 30 * time.Second}, UA: "torr-remote/0.3 (+github.com/coomteng/webos-torrserver)",
		cache: map[string]cacheEntry{}}
}

func (a *ASSRT) Name() string { return "assrt" }

// gate serialises API calls and spaces them Spacing apart. Waits (bounded by ctx) rather than failing.
func (a *ASSRT) gate(ctx context.Context) error {
	a.mu.Lock()
	wait := time.Until(a.last.Add(a.Spacing))
	a.last = time.Now()
	if wait > 0 {
		a.last = a.last.Add(wait)
	}
	a.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type assrtEnvelope struct {
	Status int    `json:"status"`
	Errmsg string `json:"errmsg"`
	Sub    struct {
		Subs []json.RawMessage `json:"subs"`
	} `json:"sub"`
}

type assrtSub struct {
	ID         json.Number     `json:"id"`
	NativeName json.RawMessage `json:"native_name"` // string or []string
	VideoName  string          `json:"videoname"`
	Subtype    string          `json:"subtype"`
	VoteScore  json.Number     `json:"vote_score"`
	UploadTime string          `json:"upload_time"`
	Lang       *struct {
		Langlist map[string]bool `json:"langlist"`
		Desc     string          `json:"desc"`
	} `json:"lang"`
	Filelist json.RawMessage `json:"filelist"` // [] of {f,s,url} or {} when empty
	URL      string          `json:"url"`
	Filename string          `json:"filename"`
}

type assrtFile struct {
	F   string `json:"f"`
	S   string `json:"s"`
	URL string `json:"url"`
}

func (a *ASSRT) call(ctx context.Context, p string, q url.Values) (*assrtEnvelope, error) {
	if a.Token == "" {
		return nil, ErrNotConfigured
	}
	if err := a.gate(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", a.Base+p+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("User-Agent", a.UA)
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env assrtEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("assrt: HTTP %d, not JSON", resp.StatusCode)
	}
	if env.Status != 0 {
		return nil, fmt.Errorf("assrt: %s (%d)", env.Errmsg, env.Status)
	}
	return &env, nil
}

func (a *ASSRT) Search(ctx context.Context, q string) ([]Result, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("empty query")
	}
	a.mu.Lock()
	if c, ok := a.cache[q]; ok && time.Since(c.at) < assrtCacheTTL {
		a.mu.Unlock()
		return c.res, nil
	}
	a.mu.Unlock()
	env, err := a.call(ctx, "/sub/search", url.Values{"q": {q}, "cnt": {"15"}, "is_file": {"1"}, "no_muxer": {"1"}, "filelist": {"1"}})
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, raw := range env.Sub.Subs {
		var s assrtSub
		if json.Unmarshal(raw, &s) != nil || s.ID.String() == "" {
			continue
		}
		r := Result{Src: "assrt", ID: s.ID.String(), Title: assrtTitle(s), Format: assrtFormat(s.Subtype), Time: s.UploadTime}
		if n, err := s.VoteScore.Int64(); err == nil {
			r.Score = int(n)
		}
		if s.Lang != nil {
			for k, on := range s.Lang.Langlist {
				if on && strings.HasPrefix(k, "lang") {
					r.Lang = append(r.Lang, strings.TrimPrefix(k, "lang"))
				}
			}
		}
		for _, f := range assrtFiles(s.Filelist) {
			r.Files = append(r.Files, f.F)
		}
		if len(r.Lang) == 0 {
			seen := map[string]bool{}
			for _, n := range append([]string{s.VideoName}, r.Files...) {
				for _, t := range LangOf(n) {
					if !seen[t] {
						seen[t] = true
						r.Lang = append(r.Lang, t)
					}
				}
			}
		}
		sort.Strings(r.Lang)
		r.Label = LangLabel(r.Lang)
		out = append(out, r)
	}
	a.mu.Lock()
	a.cache[q] = cacheEntry{time.Now(), out}
	a.mu.Unlock()
	return out, nil
}

func (a *ASSRT) Files(ctx context.Context, id string) ([]File, error) {
	env, err := a.call(ctx, "/sub/detail", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if len(env.Sub.Subs) == 0 {
		return nil, errors.New("assrt: no such subtitle")
	}
	var s assrtSub
	if err := json.Unmarshal(env.Sub.Subs[0], &s); err != nil {
		return nil, err
	}
	var out []File
	for _, f := range assrtFiles(s.Filelist) {
		if f.URL != "" {
			out = append(out, File{Name: f.F, URL: f.URL, Archive: s.URL, ArchiveName: s.Filename})
		}
	}
	if len(out) == 0 && s.URL != "" {
		out = append(out, File{Name: s.Filename, URL: s.URL})
	}
	if len(out) == 0 {
		return nil, errors.New("assrt: no downloadable file")
	}
	return out, nil
}

// Download fetches a file URL from the detail response. These are CDN links that do not count
// against the API quota, so no gate. When the per-file link answers with an error status (the
// on-the-fly extraction fails on ASSRT's side), the file is taken from the package's archive.
func (a *ASSRT) Download(ctx context.Context, f File) ([]byte, error) {
	data, err := a.fetch(ctx, f.URL, maxFileBytes)
	var se *statusError
	if errors.As(err, &se) && f.Archive != "" && f.Archive != f.URL {
		return a.fromArchive(ctx, f, se.Status)
	}
	return data, err
}

// maxFileBytes bounds one subtitle file taken from the network.
const maxFileBytes = 8 << 20

// statusError is a download answered with a status other than 200.
type statusError struct {
	Status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

// fetch GETs a CDN link and returns at most limit bytes of the body.
func (a *ASSRT) fetch(ctx context.Context, rawurl string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawurl, nil)
	if err != nil {
		return nil, downloadErr(err)
	}
	req.Header.Set("User-Agent", a.UA)
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, downloadErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// Failures are redirects to an error page that states the reason. Report where we ended up
		// and what it said; host and path only, the query carries the link's signature.
		page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		// The path is the redirect's to choose: keep it escaped and short.
		where := resp.Request.URL.Host + resp.Request.URL.EscapedPath()
		if len(where) > 120 {
			where = where[:120] + "…"
		}
		msg := fmt.Sprintf("assrt download: HTTP %d at %s", resp.StatusCode, where)
		if why := pageText(page, 200); why != "" {
			msg += ": " + why
		}
		return nil, &statusError{Status: resp.StatusCode, msg: msg}
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// fromArchive downloads the package's archive after the per-file link failed with status and
// returns the member named like f, or the whole zip or rar when no member is: the caller expands it.
// Messages name the archive by its file name only, never by its signed link; the name is the
// remote server's to choose, so it goes through msgName.
func (a *ASSRT) fromArchive(ctx context.Context, f File, status int) ([]byte, error) {
	fail := func(format string, args ...interface{}) ([]byte, error) {
		return nil, fmt.Errorf("assrt: per-file download failed (HTTP %d)"+format, append([]interface{}{status}, args...)...)
	}
	name := msgName(f.ArchiveName)
	data, err := a.fetch(ctx, f.Archive, maxArchiveBytes+1)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			return fail("; archive failed too (HTTP %d)", se.Status)
		}
		return fail("; archive failed too (%s)", cutString(strings.TrimPrefix(err.Error(), "assrt download: "), 60))
	}
	if int64(len(data)) > maxArchiveBytes {
		return fail(" and the archive is larger than %d MB: %s", maxArchiveBytes>>20, name)
	}
	switch kind := archiveKind(data); kind {
	case "zip", "rar":
		var hit *Entry
		var subtitles int
		if kind == "rar" {
			hit, subtitles, err = rarMember(ctx, data, f.Name)
		} else {
			hit, subtitles, err = zipMember(data, f.Name)
		}
		var um *unreadableMember
		switch {
		case errors.As(err, &um): // do not hand on the whole archive: another member would be picked
			return fail(" and %s in the archive could not be read", msgName(um.Name))
		case err == errRarOld:
			return fail(" and the archive is an old-format rar that needs too much memory to unpack: %s", name)
		case err == errRarBusy:
			return fail(" and the archive %s could not be read in time", name)
		case err != nil:
			return fail(" and the archive %s could not be read", name)
		case hit != nil:
			return hit.Data, nil
		case subtitles == 0:
			return fail(" and the archive %s has no subtitle file", name)
		}
		return data, nil
	case "7z":
		return fail(" and the archive is %s, which is not supported: %s", kind, name)
	}
	if IsSubtitleName(f.ArchiveName) && len(data) <= maxFileBytes { // a single-file package is the subtitle itself
		return data, nil
	}
	return fail(" and the archive %s is in an unknown format", name)
}

// msgName makes a file name from the network fit for a message that is logged and shown on the
// TV: one line without control or format characters, at most 80 bytes, "package" if nothing is left.
func msgName(s string) string {
	if s = strings.TrimSpace(cutString(oneLine(s), 80)); s != "" {
		return s
	}
	return "package"
}

// cutString returns at most max bytes of s, cut between runes.
func cutString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// downloadErr keeps the cause of a request error and drops the URL that net/http and net/url
// quote in front of it: the query carries the link's signature.
func downloadErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("assrt download: %w", ue.Err)
	}
	return fmt.Errorf("assrt download: %w", err)
}

// pageText reduces an HTML page to its text on one line: tags, comments and the contents of
// <script> and <style> dropped, entities decoded, control characters and whitespace runs
// collapsed, at most max bytes, cut between runes.
func pageText(page []byte, max int) string {
	s := strings.ToValidUTF8(string(page), "") // the read limit may have split a rune; the page may not be UTF-8
	// lower-cased for matching tag names; ASCII only, so offsets match s
	low := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		b.WriteByte(' ')
		s, low = s[i:], low[i:]
		// find the end of what this "<" opens; an unclosed one swallows the rest
		var end int
		switch {
		case strings.HasPrefix(low, "<!--"):
			end = skipTo(low, 4, "-->")
		case strings.HasPrefix(low, "<script"):
			end = skipTo(low, skipTo(low, 0, "</script"), ">")
		case strings.HasPrefix(low, "<style"):
			end = skipTo(low, skipTo(low, 0, "</style"), ">")
		default:
			end = skipTo(low, 0, ">")
		}
		s, low = s[end:], low[end:]
	}
	s = oneLine(html.UnescapeString(b.String()))
	if len(s) > max {
		for max > 0 && !utf8.RuneStart(s[max]) {
			max--
		}
		s = strings.TrimSpace(s[:max])
	}
	return s
}

// oneLine turns control and format characters into spaces and collapses whitespace runs.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// skipTo returns the offset just past the first mark in s at or after from, or len(s) if none.
func skipTo(s string, from int, mark string) int {
	if i := strings.Index(s[from:], mark); i >= 0 {
		return from + i + len(mark)
	}
	return len(s)
}

func assrtFiles(raw json.RawMessage) []assrtFile {
	var fs []assrtFile
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	json.Unmarshal(raw, &fs)
	return fs
}

func assrtTitle(s assrtSub) string {
	var native string
	var str string
	var arr []string
	if json.Unmarshal(s.NativeName, &str) == nil {
		native = str
	} else if json.Unmarshal(s.NativeName, &arr) == nil && len(arr) > 0 {
		native = arr[0]
	}
	v := s.VideoName
	if v == "不知道" {
		v = ""
	}
	switch {
	case native != "" && v != "" && !strings.Contains(v, native):
		return native + " — " + v
	case v != "":
		return v
	default:
		return native
	}
}

func assrtFormat(subtype string) string {
	t := strings.ToLower(subtype)
	switch {
	case strings.Contains(t, "srt"), strings.Contains(t, "subrip"):
		return "srt"
	case strings.Contains(t, "ass"), strings.Contains(t, "ssa"):
		return "ass"
	}
	return ""
}
