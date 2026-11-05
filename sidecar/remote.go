package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// --------------------------------------------------------------------------- phone -> TV channel
// Lifted from moontv-webos api-go/remote.go. The TV registers a channel (POST /remote/new, reusing
// its stored id) and shows a QR code for /r/<id>. The phone POSTs messages to /remote/<id>/send;
// the TV long-polls /remote/<id>/poll?since=<seq>. Everything is in memory and LAN-only.

const (
	remoteTTL      = 30 * 24 * time.Hour // a TV keeps its id in localStorage; keep channels for a month of idling
	remotePollWait = 25 * time.Second
	remoteIDChars  = "abcdefghjkmnpqrstuvwxyz23456789"
	remoteKeep     = 20
)

var remoteIDRe = regexp.MustCompile(`^[A-Za-z0-9]{4,16}$`)
var hashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// remoteMsg is one message queued for the TV.
//   action: "play"  -> open the player for hash/index (index = TorrServer file id, 1-based)
//           "added" -> a torrent was added on the phone; the TV refreshes its list
//           "text"  -> free text (reserved)
type remoteMsg struct {
	Seq    int     `json:"seq"`
	Action string  `json:"action"`
	Hash   string  `json:"hash,omitempty"`
	Index  int     `json:"index,omitempty"`
	Title  string  `json:"title,omitempty"`
	Text   string  `json:"text,omitempty"`
	T      float64 `json:"t"`
}

// remoteState is what the TV last reported about itself (shown on the phone page).
type remoteState struct {
	View  string `json:"view"`
	Title string `json:"title"`
	Hash  string `json:"hash,omitempty"`
	Index int    `json:"index,omitempty"`
	T     int64  `json:"t"`
}

type remoteChannel struct {
	id    string
	mu    sync.Mutex
	cond  *sync.Cond
	seq   int
	msgs  []remoteMsg
	state *remoteState
	t     time.Time
}

func newRemoteChannel(id string, now time.Time) *remoteChannel {
	ch := &remoteChannel{id: id, msgs: []remoteMsg{}, t: now}
	ch.cond = sync.NewCond(&ch.mu)
	return ch
}

type Remote struct {
	mu sync.Mutex
	ch map[string]*remoteChannel
}

func newRemote() *Remote { return &Remote{ch: map[string]*remoteChannel{}} }

func (rm *Remote) newID() string {
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(remoteIDChars))))
		if err != nil {
			panic(err)
		}
		b[i] = remoteIDChars[n.Int64()]
	}
	return string(b)
}

func (rm *Remote) count() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return len(rm.ch)
}

// channel returns channel cid (expiring idle ones first). With create, a missing/invalid cid
// yields a fresh channel with a generated id. Returns nil when not found and !create.
func (rm *Remote) channel(cid string, create bool) *remoteChannel {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	now := time.Now()
	for k, v := range rm.ch {
		if now.Sub(v.t) > remoteTTL {
			delete(rm.ch, k)
		}
	}
	var ch *remoteChannel
	if cid != "" {
		ch = rm.ch[cid]
	}
	if ch == nil && create {
		if cid == "" || !remoteIDRe.MatchString(cid) {
			cid = rm.newID()
			for rm.ch[cid] != nil {
				cid = rm.newID()
			}
		}
		ch = newRemoteChannel(cid, now)
		rm.ch[cid] = ch
	}
	if ch != nil {
		ch.t = now
	}
	return ch
}

func (rm *Remote) send(ch *remoteChannel, m remoteMsg) int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.seq++
	m.Seq = ch.seq
	m.T = float64(time.Now().UnixNano()) / 1e9
	ch.msgs = append(ch.msgs, m)
	if len(ch.msgs) > remoteKeep {
		ch.msgs = append([]remoteMsg{}, ch.msgs[len(ch.msgs)-remoteKeep:]...)
	}
	ch.cond.Broadcast()
	return ch.seq
}

// poll waits up to timeout for messages with seq > since.
func (rm *Remote) poll(ch *remoteChannel, since int, timeout time.Duration) map[string]interface{} {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		ch.mu.Lock()
		ch.cond.Broadcast()
		ch.mu.Unlock()
	})
	defer timer.Stop()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	for {
		pending := []remoteMsg{}
		for _, m := range ch.msgs {
			if m.Seq > since {
				pending = append(pending, m)
			}
		}
		if len(pending) > 0 || !time.Now().Before(deadline) {
			return map[string]interface{}{"seq": ch.seq, "messages": pending}
		}
		ch.cond.Wait()
	}
}

// qrSVG renders data as a single-path SVG (1 unit per module, 2-module quiet zone).
func qrSVG(data string) ([]byte, error) {
	qr, err := qrcode.New(data, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	qr.DisableBorder = true
	bm := qr.Bitmap()
	const border = 2
	n := len(bm)
	dim := n + 2*border
	var b strings.Builder
	fmt.Fprintf(&b, `<svg width="%dmm" height="%dmm" version="1.1" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg">`, dim, dim, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, dim, dim)
	b.WriteString(`<path d="`)
	for y, row := range bm {
		for x, on := range row {
			if on {
				fmt.Fprintf(&b, "M%d,%dH%dV%dH%dz", x+border, y+border, x+border+1, y+border+1, x+border)
			}
		}
	}
	b.WriteString(`" fill="#000000" stroke="none"/></svg>`)
	return []byte(b.String()), nil
}

func hostOf(r *http.Request, a *App) string {
	if r.Host != "" {
		return r.Host
	}
	return "localhost:" + a.cfg.Port
}

func (a *App) phoneURL(r *http.Request, id string) string {
	return fmt.Sprintf("http://%s/r/%s", hostOf(r, a), id)
}

// remoteGet handles GET /remote/<id>/{qr.svg,poll,state}.
func (a *App) remoteGet(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(p, "/") // ['', 'remote', '<id>', '<op>']
	if len(parts) != 4 {
		sendError(w, r, 404, "not found")
		return
	}
	ch := a.remote.channel(parts[2], false)
	if ch == nil {
		sendError(w, r, 404, "no such channel")
		return
	}
	switch parts[3] {
	case "qr.svg":
		svg, err := qrSVG(a.phoneURL(r, ch.id))
		if err != nil {
			sendError(w, r, 500, err.Error())
			return
		}
		send(w, r, 200, svg, "image/svg+xml", 86400)
	case "poll":
		since := atoi(r.URL.Query().Get("since"), 0)
		sendJSON(w, r, 200, a.remote.poll(ch, since, remotePollWait))
	case "state":
		ch.mu.Lock()
		st := ch.state
		ch.mu.Unlock()
		if st == nil {
			sendJSON(w, r, 200, map[string]interface{}{})
		} else {
			sendJSON(w, r, 200, st)
		}
	default:
		sendError(w, r, 404, "not found")
	}
}

// remoteNew handles POST /remote/new: reuse body.id if it is a live/valid channel id, else create.
func (a *App) remoteNew(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
	ch := a.remote.channel(strings.TrimSpace(str(body["id"])), true)
	ch.mu.Lock()
	seq := ch.seq
	ch.mu.Unlock()
	sendJSON(w, r, 200, map[string]interface{}{"id": ch.id, "seq": seq, "url": a.phoneURL(r, ch.id)})
}

// remotePost handles POST /remote/<id>/{send,state}.
func (a *App) remotePost(w http.ResponseWriter, r *http.Request, p string, body map[string]interface{}) {
	parts := strings.Split(p, "/")
	var ch *remoteChannel
	if len(parts) == 4 {
		ch = a.remote.channel(parts[2], false)
	}
	if ch == nil {
		sendError(w, r, 404, "no such channel")
		return
	}
	switch parts[3] {
	case "send":
		action := str(body["action"])
		if action != "play" && action != "added" && action != "text" {
			sendError(w, r, 400, "unknown action")
			return
		}
		hash := strings.ToLower(strings.TrimSpace(str(body["hash"])))
		if (action == "play" || action == "added") && !hashRe.MatchString(hash) {
			sendError(w, r, 400, "bad hash")
			return
		}
		m := remoteMsg{Action: action, Hash: hash, Index: atoi(str(body["index"]), 0),
			Title: truncRunes(str(body["title"]), 200), Text: truncRunes(str(body["text"]), 500)}
		seq := a.remote.send(ch, m)
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "seq": seq})
	case "state":
		st := &remoteState{View: truncRunes(str(body["view"]), 20), Title: truncRunes(str(body["title"]), 200),
			Hash: strings.ToLower(truncRunes(str(body["hash"]), 40)), Index: atoi(str(body["index"]), 0), T: time.Now().Unix()}
		ch.mu.Lock()
		ch.state = st
		ch.mu.Unlock()
		sendJSON(w, r, 200, map[string]bool{"ok": true})
	default:
		sendError(w, r, 404, "not found")
	}
}

// servePhonePage handles GET /r/<id>.
func (a *App) servePhonePage(w http.ResponseWriter, r *http.Request, id string) {
	if !remoteIDRe.MatchString(id) {
		sendError(w, r, 404, "not found")
		return
	}
	idJSON, _ := json.Marshal(id)
	tsJSON, _ := json.Marshal(a.cfg.TorrServer)
	page := strings.Replace(phonePage, "__ID__", string(idJSON), 1)
	page = strings.Replace(page, "__TS__", string(tsJSON), 1)
	send(w, r, 200, []byte(page), "text/html; charset=utf-8", 0)
}
