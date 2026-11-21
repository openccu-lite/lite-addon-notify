// notify is the lite-addon-notify daemon: it reads the system's warnings, the devices' service
// messages, datapoint values and a few system facts through openccu-lite's API, and sends
// messages about them to the channels the user set up (Pushover, Telegram, email, ntfy, Gotify,
// webhooks, Discord, Slack, Matrix, Signal, MQTT). It serves its page and API on the loopback
// behind lighttpd's proxy of /addons/notify/, and an inbound webhook at /inbound on the same
// port. openccu-lite only; no listening port beyond the loopback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"

	"github.com/openccu-lite/lite-addon-notify/internal/channel"
	"github.com/openccu-lite/lite-addon-notify/internal/config"
	"github.com/openccu-lite/lite-addon-notify/internal/deliver"
	"github.com/openccu-lite/lite-addon-notify/internal/engine"
	"github.com/openccu-lite/lite-addon-notify/internal/httpapi"
	"github.com/openccu-lite/lite-addon-notify/internal/source"
	"github.com/openccu-lite/lite-addon-notify/internal/texts"
)

// version is set by the build (-ldflags "-X main.version=…").
var version = "dev"

type options struct {
	addonDir, confDir, id string
	port                  int
	base, basePath, www   string
	tokenFile, notices    string
	noSession             bool
}

func main() {
	log.SetFlags(0) // the journal adds the time
	var o options
	flag.StringVar(&o.addonDir, "addon-dir", "/usr/local/addons/notify", "the addon's directory (app/, var/)")
	flag.StringVar(&o.confDir, "conf-dir", "/usr/local/etc/config/addons/notify", "the configuration's directory (notify.json; mode 0700)")
	flag.StringVar(&o.id, "id", "notify", "the addon id (token file, base path)")
	flag.IntVar(&o.port, "port", 8300, "the loopback port lighttpd proxies to; the inbound webhook is on it too")
	flag.StringVar(&o.base, "system", "http://127.0.0.1", "openccu-lite's URL")
	flag.StringVar(&o.basePath, "base-path", "", "the path the page is served at (/addons/<id> by default)")
	flag.StringVar(&o.www, "www", "", "the built page (<addon-dir>/app by default)")
	flag.StringVar(&o.tokenFile, "token-file", "", "a token file for the system's API (the addon's own token by default)")
	flag.StringVar(&o.notices, "notices", "http://127.0.0.1:8183", "where the addon posts its own Status-page notices (\"\": nowhere)")
	flag.BoolVar(&o.noSession, "no-session-check", false, "serve the page without the session check (development only)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if o.basePath == "" {
		o.basePath = "/addons/" + o.id
	}
	if o.www == "" {
		o.www = filepath.Join(o.addonDir, "app")
	}
	if err := run(o); err != nil {
		log.Fatalf("notify: %v", err)
	}
}

// stateFile is what the daemon remembers between starts: what is open, what was announced,
// what waits in the batches.
type stateFile struct {
	Engine  json.RawMessage `json:"engine"`
	Sources json.RawMessage `json:"sources"`
}

func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "openccu-lite"
	}
	return h
}

func run(o options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logf := func(format string, args ...any) { log.Printf("notify: "+format, args...) }
	channel.UserAgent = "lite-addon-notify/" + version

	store, err := config.Open(o.confDir)
	if err != nil {
		return err
	}
	varDir := filepath.Join(o.addonDir, "var")
	if err := os.MkdirAll(varDir, 0o755); err != nil {
		return err
	}

	// the configuration as the engine and the sources read it, many times a second at most:
	// a copy held here, replaced on every change
	var current atomic.Pointer[config.Config]
	load := func() { c := store.Get(); current.Store(&c) }
	load()
	cfg := func() config.Config { return *current.Load() }

	var token addonkit.TokenSource
	if o.tokenFile != "" {
		token = &addonkit.FileToken{Path: o.tokenFile}
	} else {
		token = addonkit.AddonToken(o.id)
	}
	lite := litesource.New(o.base, token)
	// wait for the system: at boot its web server answers 503 until occulited is up, and the
	// token may not exist yet; never a reason to give up
	if _, err := lite.Await(ctx, addonkit.IsOpenccuLite() || o.tokenFile != ""); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("this system is not openccu-lite: %w", err)
	}

	var src *source.Sources
	eng := engine.New(engine.Options{Config: cfg, System: hostname, Log: logf,
		Open:     func(lang string) engine.Open { return src.Open(lang) },
		Describe: func(iface, address, lang string) (string, []string) { return src.Describe(iface, address, lang) }})
	src = source.New(source.Options{Base: o.base, HTTP: &http.Client{Transport: &addonkit.AuthTransport{Source: token}}, Lite: lite, Engine: eng, Config: cfg, BootID: bootID, Log: logf})

	statePath := filepath.Join(varDir, "state.json")
	if b, err := os.ReadFile(statePath); err == nil {
		var st stateFile
		if json.Unmarshal(b, &st) == nil {
			eng.Import(st.Engine)
			src.Import(st.Sources)
		} else {
			logf("the state file is not readable: starting from what the system says now")
		}
	}
	save := func(force bool) {
		e, ed := eng.Export()
		s, sd := src.Export()
		if !ed && !sd && !force {
			return
		}
		b, _ := json.Marshal(stateFile{Engine: e, Sources: s})
		tmp := statePath + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			logf("the state could not be written: %v", err)
			return
		}
		_ = os.Rename(tmp, statePath)
	}

	nt := &notices{base: o.notices, tokenFile: filepath.Join(addonkit.TokenDir, o.id), id: o.id, log: logf, lang: func() string { return cfg().Settings.Language }}
	if o.tokenFile != "" {
		nt.base = "" // development: no system to post to
	}
	disp := deliver.New(deliver.Options{Context: ctx, HistoryFile: filepath.Join(varDir, "history.json"), Log: logf, Failing: nt.channel,
		Channel:  func(id string) (config.Channel, bool) { return store.Channel(id) },
		Settings: func() config.Settings { return cfg().Settings }})

	kick := make(chan struct{}, 1)
	wake := func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	src.OnEvent = wake
	store.OnChange = func() { load(); src.Kick(); wake(); go nt.sync(cfg()) }

	go disp.Run()
	go src.Run(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		lastSave := time.Now()
		for {
			now := time.Now()
			for _, out := range eng.Tick(now) {
				disp.Enqueue(out.Channels, out.Message)
			}
			if time.Since(lastSave) > 5*time.Second {
				save(false)
				lastSave = time.Now()
			}
			// the sources hand their events to the engine on their own; half a minute at most
			// passes before the loop looks at what waits
			wait := 30 * time.Second
			if next := eng.Next(now); !next.IsZero() {
				if d := time.Until(next); d < wait {
					wait = max(d, 50*time.Millisecond)
				}
			}
			if eng.PendingCount() > 0 && wait > 5*time.Second {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				save(true)
				return
			case <-kick:
			case <-time.After(wait):
			}
		}
	}()

	wrap := func(h http.Handler) http.Handler { return h }
	if !o.noSession {
		c := addonkit.NewChecker(addonkit.SessionOptions{Base: o.base, Need: addonkit.LevelAdmin, AddonID: o.id, OnRefused: func(r *http.Request, reason string) {
			logf("refused %s %s: %s", r.Method, r.URL.Path, reason)
		}})
		wrap = c.Handler
	}
	h := httpapi.New(httpapi.Options{BasePath: o.basePath, WWW: o.www, Store: store, Engine: eng, Sources: src, Deliver: disp, Wrap: wrap, Version: version, Started: time.Now(),
		Kick: wake, System: hostname, InboundURL: fmt.Sprintf("http://127.0.0.1:%d%s", o.port, httpapi.InboundPath), Log: logf})
	l, err := addonkit.Listen(o.port, "")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	c := cfg()
	logf("%s: serving %s on %s; %d channels, %d rules", version, o.basePath, l.Addr(), len(c.Channels), len(c.Rules))
	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		return err
	}
	<-done
	logf("stopping")
	os.Stdout.Sync()
	return nil
}

// notices posts the addon's own notices to the Status page: a channel whose messages are
// given up is one, cleared when the channel works again. The credential is the addon's control
// token, which the system mints for every addon.
type notices struct {
	base, tokenFile, id string
	lang                func() string
	log                 func(string, ...any)
	mu                  sync.Mutex
	raised              map[string]config.Channel
}

// sync clears the notices of channels that were removed or switched off.
func (n *notices) sync(c config.Config) {
	n.mu.Lock()
	var gone []config.Channel
	for id, ch := range n.raised {
		found := false
		for _, cur := range c.Channels {
			if cur.ID == id && cur.Enabled {
				found = true
			}
		}
		if !found {
			gone = append(gone, ch)
		}
	}
	n.mu.Unlock()
	for _, ch := range gone {
		n.channel(ch, false, "")
	}
}

func (n *notices) channel(ch config.Channel, failing bool, reason string) {
	if n.base == "" {
		return
	}
	n.mu.Lock()
	if n.raised == nil {
		n.raised = map[string]config.Channel{}
	}
	if failing {
		n.raised[ch.ID] = ch
	} else {
		delete(n.raised, ch.ID)
	}
	n.mu.Unlock()
	b, err := os.ReadFile(n.tokenFile)
	if err != nil {
		n.log("no control token (%v): the Status page is not told about channel %s", err, ch.ID)
		return
	}
	url := n.base + "/api/system/v1/addon-notices/channel-" + ch.ID
	var req *http.Request
	if failing {
		if r := []rune(reason); len(r) > 900 {
			reason = string(r[:900])
		}
		body, _ := json.Marshal(map[string]any{"severity": "warning",
			"title": map[string]string{"de": texts.T("de", "channel-failing", ch.Name), "en": texts.T("en", "channel-failing", ch.Name)},
			"text":  map[string]string{"de": reason, "en": reason}, "link": "/addon-settings/" + n.id})
		req, _ = http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(http.MethodDelete, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		n.log("the Status page's notice for channel %s: %v", ch.ID, err)
		return
	}
	res.Body.Close()
	if res.StatusCode >= 300 && !(res.StatusCode == 404 && !failing) {
		n.log("the Status page's notice for channel %s: %s", ch.ID, res.Status)
	}
}
