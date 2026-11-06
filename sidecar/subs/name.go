// Package subs turns subtitle files from the wild (SRT/ASS in GBK/UTF-16, inside zips, named after
// release groups) into WebVTT the TV can draw, and talks to subtitle sites for the search.
package subs

import (
	"path"
	"regexp"
	"strings"
)

var (
	epRe    = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*E(\d{1,3})\b|\b(\d{1,2})x(\d{2,3})\b|第\s*(\d{1,3})\s*[集话話]|\bE(?:P)?(\d{1,3})\b`)
	yearRe  = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	noiseRe = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|hdr|hdr10|dv|dolby|vision|bluray|blu-ray|bdrip|brrip|web-?dl|webrip|hdtv|dvdrip|remux|x264|x265|h\.?264|h\.?265|hevc|avc|aac|ac3|dts|ddp?\d?\.?\d?|atmos|truehd|flac|10bit|8bit|hdrip|cam|ts|proper|repack|internal|limited|extended|unrated|imax|multi|dual|audio|amzn|nf|dsnp|hmax|atvp|itunes|chs|cht|eng|中字|国语|粤语|双语)\b`)
	sepRe   = regexp.MustCompile(`[._\[\]()【】（）{}+]+`)
	spaceRe = regexp.MustCompile(`\s+`)
	cjkRe   = regexp.MustCompile(`^[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}0-9\s:：·、，。！？~-]+[.\s]+`)
	latinRe = regexp.MustCompile(`[A-Za-z]{2,}`)
	groupRe = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)
)

// Episode is the SxxEyy pulled out of a file name; Season is 0 when only an episode number was found.
type Episode struct{ Season, Number int }

// ParseEpisode returns the episode encoded in a file/release name, if any.
func ParseEpisode(name string) (Episode, bool) {
	m := epRe.FindStringSubmatch(name)
	if m == nil {
		return Episode{}, false
	}
	switch {
	case m[1] != "":
		return Episode{atoi(m[1]), atoi(m[2])}, true
	case m[3] != "":
		return Episode{atoi(m[3]), atoi(m[4])}, true
	case m[5] != "":
		return Episode{0, atoi(m[5])}, true
	default:
		return Episode{0, atoi(m[6])}, true
	}
}

// SearchQuery reduces a release file name to something a subtitle site understands:
// "沙丘2.Dune.Part.Two.2024.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX.mkv" -> "Dune Part Two 2024".
// Series keep their episode: "Shogun.S01E03.1080p.WEB.h264-ETHEL.mkv" -> "Shogun S01E03".
func SearchQuery(file string) string {
	base := path.Base(strings.ReplaceAll(file, "\\", "/"))
	if ext := path.Ext(base); len(ext) <= 5 {
		base = strings.TrimSuffix(base, ext)
	}
	ep, hasEp := ParseEpisode(base)
	base = groupRe.ReplaceAllString(base, "") // "[SubsPlease] Frieren - 12" -> "Frieren - 12"
	// drop a leading CJK title glued to a Latin one ("沙丘2.Dune..."); keep CJK-only names
	if loc := cjkRe.FindStringIndex(base); loc != nil && loc[1] < len(base) && latinRe.MatchString(base[loc[1]:]) {
		base = base[loc[1]:]
	}
	s := sepRe.ReplaceAllString(base, " ")
	s = strings.ReplaceAll(s, "-", " ")
	// cut everything after the first noise token / year / episode marker; what's left is the title
	title := s
	cut := len(s)
	if loc := noiseRe.FindStringIndex(s); loc != nil && loc[0] < cut {
		cut = loc[0]
	}
	if loc := yearRe.FindStringIndex(s); loc != nil && loc[0] < cut && loc[0] > 0 {
		cut = loc[0]
	}
	if hasEp {
		if loc := epRe.FindStringIndex(s); loc != nil && loc[0] < cut && loc[0] > 0 {
			cut = loc[0]
		}
	}
	title = strings.TrimSpace(s[:cut])
	if title == "" {
		title = strings.TrimSpace(s)
	}
	out := []string{title}
	if hasEp {
		if ep.Season > 0 {
			out = append(out, sprintf("S%02dE%02d", ep.Season, ep.Number))
		} else {
			out = append(out, sprintf("E%02d", ep.Number))
		}
	} else if y := yearRe.FindString(s); y != "" && !strings.Contains(title, y) {
		out = append(out, y)
	}
	return spaceRe.ReplaceAllString(strings.TrimSpace(strings.Join(out, " ")), " ")
}
