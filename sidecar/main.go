// torr-remote: the small sidecar next to TorrServer for the webOS TV app.
//
// It does three things and nothing else (TorrServer itself is called directly by the TV
// app and the phone page; its CORS is open):
//
//   - phone -> TV channel: the TV registers a channel and shows a QR code to /r/<id>;
//     the phone page POSTs {action, hash, index, ...} to /remote/<id>/send and the TV
//     long-polls /remote/<id>/poll. Memory only, LAN only.
//   - /r/<id>: the phone page (paste a magnet, "cache" or "watch").
//   - /tv/ and /sim: static hosting of the TV app and the browser simulator, plus /config
//     so both know where TorrServer lives.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	TorrServer string // public URL of TorrServer as seen from the TV/phone, e.g. http://192.168.0.9:8090
	Listen     string
	Port       string
	AppDir     string
	SimFile    string
	Verbose    bool
}

func loadConfig() Config {
	get := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	return Config{
		TorrServer: strings.TrimRight(get("TORR_SERVER", "http://127.0.0.1:8090"), "/"),
		Listen:     get("TORR_LISTEN", "0.0.0.0"),
		Port:       get("TORR_PORT", "8791"),
		AppDir:     os.Getenv("TORR_APP_DIR"),
		SimFile:    os.Getenv("TORR_SIM"),
		Verbose:    os.Getenv("TORR_VERBOSE") != "",
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[torr-remote] ")
	cfg := loadConfig()
	app := newApp(cfg)
	addr := cfg.Listen + ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// no WriteTimeout: /remote/<id>/poll holds connections for up to 25 s
		IdleTimeout: 120 * time.Second,
	}
	log.Printf("listening on %s, TorrServer at %s", addr, cfg.TorrServer)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
