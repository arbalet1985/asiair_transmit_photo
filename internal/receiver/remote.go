package receiver

import (
	"compress/gzip"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// Remote access: the web page from a phone (or another computer), over the
// internet on the agent port, or in the user's tailnet. Unlike the page on
// this computer it always needs a password, and the settings of this
// computer (folders, service, token, network) stay local.

const (
	sessionCookie  = "asiair_session"
	sessionTTL     = 90 * 24 * time.Hour
	minPasswordLen = 8
	loginMaxFails  = 5                // failed logins from one address …
	loginWindow    = 15 * time.Minute // … within this time lock it for loginWindow
	loginGlobalMax = 30               // failures from all addresses within loginWindow before every login is slowed down
)

// loginDelay slows down each wrong password.
var loginDelay = time.Second

type ctxKey int

const remoteKey ctxKey = 1

// isRemote reports a request that came through remote access.
func isRemote(r *http.Request) bool { return r.Context().Value(remoteKey) != nil }

// localOnly lists the page API that is not served remotely.
var localOnly = map[string]bool{
	"/ui/api/connect":   true, // the agent token
	"/ui/api/token":     true,
	"/ui/api/network":   true,
	"/ui/api/tailscale": true,
	"/ui/api/remote":    true,
	"/ui/api/dirs":      true, // folders of this computer
	"/ui/api/mkdir":     true,
	"/ui/api/destdir":   true,
	"/ui/api/startup":   true,
}

// ---------- password ----------

const pbkdf2Iter = 210000

// HashPassword returns the form of the password kept in receiver.yaml.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 10_000_000 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// ---------- sessions ----------

// sessions are the logged-in browsers. Only hashes of the cookies are kept,
// in a file, so a restart or an update does not log the phone out.
type sessions struct {
	mu   sync.Mutex
	file string
	m    map[string]time.Time // sha256 of the cookie -> expiry
}

func loadSessions(file string) *sessions {
	s := &sessions{file: file, m: map[string]time.Time{}}
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &s.m)
	}
	return s
}

func sessionKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// saveLocked writes the sessions; caller holds s.mu.
func (s *sessions) saveLocked() {
	now := time.Now()
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	b, _ := json.Marshal(s.m)
	tmp := s.file + ".new"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.file)
	}
}

func (s *sessions) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sessionKey(tok)] = time.Now().Add(sessionTTL)
	s.saveLocked()
	return tok
}

// valid checks a cookie; a session in use is extended (at most once a day).
func (s *sessions) valid(tok string) bool {
	if tok == "" {
		return false
	}
	k := sessionKey(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[k]
	now := time.Now()
	if !ok || now.After(exp) {
		return false
	}
	if exp.Sub(now) < sessionTTL-24*time.Hour {
		s.m[k] = now.Add(sessionTTL)
		s.saveLocked()
	}
	return true
}

func (s *sessions) remove(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, sessionKey(tok))
	s.saveLocked()
}

func (s *sessions) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = map[string]time.Time{}
	s.saveLocked()
}

func (s *sessions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, now := 0, time.Now()
	for _, exp := range s.m {
		if now.Before(exp) {
			n++
		}
	}
	return n
}

// ---------- guessing protection ----------

type limiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time // by client address
	all   []time.Time
	now   func() time.Time
}

func newLimiter() *limiter { return &limiter{fails: map[string][]time.Time{}, now: time.Now} }

func recent(list []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(list) && now.Sub(list[i]) > loginWindow {
		i++
	}
	return list[i:]
}

// wait returns how long the address is still locked out (0 = may try).
func (l *limiter) wait(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := recent(l.fails[ip], now)
	l.fails[ip] = f
	if len(f) >= loginMaxFails {
		return loginWindow - now.Sub(f[len(f)-loginMaxFails])
	}
	return 0
}

// fail records a wrong password; it tells whether logins are slowed down
// for everyone (many failures from many addresses).
func (l *limiter) fail(ip string) (flood bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.fails[ip] = append(recent(l.fails[ip], now), now)
	l.all = append(recent(l.all, now), now)
	for k, v := range l.fails {
		if len(recent(v, now)) == 0 {
			delete(l.fails, k)
		}
	}
	return len(l.all) > loginGlobalMax
}

func (l *limiter) ok(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- handlers ----------

// remoteOn tells whether remote access through the channel is enabled
// ("internet" or "tailscale").
func (s *Server) remoteOn(channel string) bool {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if channel == "tailscale" {
		return bool(s.cfg.RemoteTailscale)
	}
	return bool(s.cfg.RemoteInternet)
}

func (s *Server) remotePassword() string {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	return s.cfg.RemotePassword
}

func (s *Server) remoteTOTP() string {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	return s.cfg.RemoteTOTP
}

// agentPortHandler serves the agent API and, when remote access through
// the channel is on, the page.
func (s *Server) agentPortHandler(channel string) http.Handler {
	api := s.apiHandler()
	page := s.remoteHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		if !s.remoteOn(channel) {
			http.NotFound(w, r)
			return
		}
		page.ServeHTTP(w, r)
	})
}

// remoteHandler is the page for remote access: the same page, behind a
// login, without the settings of this computer.
func (s *Server) remoteHandler() http.Handler {
	page := s.uiMux()
	mux := http.NewServeMux()
	for _, p := range publicFiles {
		mux.HandleFunc(p, s.handleStatic)
	}
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/login/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"totp": s.remoteTOTP() != ""})
	})
	mux.HandleFunc("/logout", s.handleLogout)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(sessionCookie)
		if s.remotePassword() == "" || c == nil || !s.sess.valid(c.Value) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "нужно войти", http.StatusUnauthorized)
			return
		}
		if localOnly[r.URL.Path] {
			http.Error(w, "доступно только на компьютере с приёмником", http.StatusForbidden)
			return
		}
		page.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), remoteKey, true)))
	}))
	return securityHeaders(jsonPosts(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.handleStatic(w, r)
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"` // one-time code, when enabled
	}
	if !readJSON(w, r, &req) {
		return
	}
	hash, secret := s.remotePassword(), s.remoteTOTP()
	if hash == "" {
		http.Error(w, "пароль для входа с телефона не задан: задайте его на странице приёмника на домашнем компьютере (блок «Доступ с телефона»)", http.StatusForbidden)
		return
	}
	ip := clientIP(r)
	if d := s.lim.wait(ip); d > 0 {
		http.Error(w, fmt.Sprintf("слишком много неверных попыток, повторите через %d мин", int(d.Minutes())+1), http.StatusTooManyRequests)
		return
	}
	// both are checked before answering: a wrong answer does not tell
	// whether the password was right
	pwOK := checkPassword(hash, req.Password)
	codeOK := secret == "" || pwOK && s.totp.check(secret, req.Code, time.Now())
	if !pwOK || !codeOK {
		flood := s.lim.fail(ip)
		msg := "неверный пароль"
		if secret != "" {
			msg = "неверный пароль или код"
			if pwOK {
				log.Printf("Приёмник: неверный одноразовый код для входа с телефона от %s", ip)
			} else {
				log.Printf("Приёмник: неверный пароль для входа с телефона от %s", ip)
			}
		} else {
			log.Printf("Приёмник: неверный пароль для входа с телефона от %s", ip)
		}
		if flood {
			time.Sleep(3 * loginDelay)
		} else {
			time.Sleep(loginDelay)
		}
		http.Error(w, msg, http.StatusUnauthorized)
		return
	}
	s.lim.ok(ip)
	tok := s.sess.create()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(sessionTTL / time.Second),
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	log.Printf("Приёмник: вход с телефона с %s", ip)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	if c, _ := r.Cookie(sessionCookie); c != nil {
		s.sess.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// ---------- page files for installing on a phone ----------

var publicFiles = []string{"/manifest.webmanifest", "/sw.js", "/icon-192.png", "/icon-512.png", "/apple-touch-icon.png"}

var staticTypes = map[string]string{
	".webmanifest": "application/manifest+json",
	".js":          "text/javascript; charset=utf-8",
	".png":         "image/png",
	".html":        "text/html; charset=utf-8",
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "login" {
		name = "login.html"
	}
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	i := strings.LastIndexByte(name, '.')
	w.Header().Set("Content-Type", staticTypes[name[i:]])
	if name == "sw.js" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Write(b)
}

// ---------- compression ----------

type gzipWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g gzipWriter) Write(b []byte) (int, error) { return g.zw.Write(b) }

// gzipped compresses the answer for browsers that accept it: the file list
// and the previews are large and the phone may be on mobile internet.
func gzipped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer zw.Close()
		next(gzipWriter{w, zw}, r)
	}
}

// ---------- settings (on this computer only) ----------

type remoteInfo struct {
	Remote      bool `json:"remote"` // this page is open remotely
	PasswordSet bool `json:"password_set"`
	TOTP        bool `json:"totp"` // one-time codes are asked after the password
	// TOTPSetup: the new secret being set up (in the answer to "totp-begin")
	TOTPSetup  *totpSetup `json:"totp_setup,omitempty"`
	Internet   bool       `json:"internet"`
	Tailscale  bool       `json:"tailscale"`
	Sessions   int        `json:"sessions"`
	Port       string     `json:"port"`
	InternetUp bool       `json:"internet_up"` // the agent port is open
	TSRunning  bool       `json:"ts_running"`
	TSName     string     `json:"ts_name,omitempty"`
	TSURL      string     `json:"ts_url,omitempty"` // https with a real certificate
	TSError    string     `json:"ts_error,omitempty"`
}

func (s *Server) remoteInfo(r *http.Request) remoteInfo {
	s.remoteMu.Lock()
	info := remoteInfo{Remote: isRemote(r), PasswordSet: s.cfg.RemotePassword != "", TOTP: s.cfg.RemoteTOTP != "",
		Internet: bool(s.cfg.RemoteInternet), Tailscale: bool(s.cfg.RemoteTailscale)}
	info.TSURL, info.TSError = s.tsWebURL, s.tsWebErr
	s.remoteMu.Unlock()
	info.Sessions = s.sess.count()
	info.Port = s.apiPort()
	s.netMu.Lock()
	info.InternetUp = s.netLn != nil
	s.netMu.Unlock()
	ts := s.tailscaleInfo()
	info.TSRunning = ts.State == tsnode.StateRunning
	info.TSName = ts.Name
	return info
}

// handleUIRemote: {"action": "password", "password": "…"} |
// "internet-on" | "internet-off" | "tailscale-on" | "tailscale-off" | "logout-all"
func (s *Server) handleUIRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action   string `json:"action"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	save := func(values map[string]string) bool {
		if s.cfg.Path == "" {
			return true
		}
		if err := config.UpdateReceiverFile(s.cfg.Path, values); err != nil {
			http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
			return false
		}
		return true
	}
	switch req.Action {
	case "password":
		if utf8.RuneCountInString(req.Password) < minPasswordLen {
			http.Error(w, fmt.Sprintf("пароль слишком короткий: нужно не меньше %d символов", minPasswordLen), http.StatusBadRequest)
			return
		}
		hash, err := HashPassword(req.Password)
		if err != nil || !save(map[string]string{"remote_password": hash}) {
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		s.remoteMu.Lock()
		s.cfg.RemotePassword = hash
		s.remoteMu.Unlock()
		s.sess.clear() // phones log in again with the new password
		log.Printf("Приёмник: задан новый пароль для входа с телефона")
	case "internet-on", "internet-off":
		on := req.Action == "internet-on"
		if !save(map[string]string{"remote_internet": strconv.FormatBool(on)}) {
			return
		}
		s.remoteMu.Lock()
		s.cfg.RemoteInternet = config.Bool(on)
		s.remoteMu.Unlock()
		log.Printf("Приёмник: страница через интернет: %s", onOff(on))
	case "tailscale-on", "tailscale-off":
		on := req.Action == "tailscale-on"
		if !save(map[string]string{"remote_tailscale": strconv.FormatBool(on)}) {
			return
		}
		s.remoteMu.Lock()
		s.cfg.RemoteTailscale = config.Bool(on)
		s.remoteMu.Unlock()
		if on {
			s.startTSWeb()
		} else {
			s.stopTSWeb()
		}
		log.Printf("Приёмник: страница через Tailscale: %s", onOff(on))
	case "totp-begin":
		secret := newTOTPSecret()
		uri := totpURI(secret, s.totpAccount())
		qr, err := totpQR(uri)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.remoteMu.Lock()
		s.totpNew = secret
		s.remoteMu.Unlock()
		info := s.remoteInfo(r)
		info.TOTPSetup = &totpSetup{Secret: groupSecret(secret), URI: uri, QR: qr}
		writeJSON(w, info)
		return
	case "totp-confirm":
		s.remoteMu.Lock()
		secret := s.totpNew
		s.remoteMu.Unlock()
		if secret == "" {
			http.Error(w, "начните настройку заново", http.StatusBadRequest)
			return
		}
		var g totpGuard
		if !g.check(secret, req.Code, time.Now()) {
			http.Error(w, "код не подошёл: проверьте, что время на телефоне точное, и введите новый код", http.StatusBadRequest)
			return
		}
		if !save(map[string]string{"remote_totp": secret}) {
			return
		}
		s.remoteMu.Lock()
		s.cfg.RemoteTOTP, s.totpNew = secret, ""
		s.remoteMu.Unlock()
		s.totp.mu.Lock()
		s.totp.last = g.last // the code just typed cannot log in
		s.totp.mu.Unlock()
		s.sess.clear() // phones log in again, now with a code
		log.Printf("Приёмник: вход с телефона — включены одноразовые коды")
	case "totp-off":
		if !save(map[string]string{"remote_totp": ""}) {
			return
		}
		s.remoteMu.Lock()
		s.cfg.RemoteTOTP, s.totpNew = "", ""
		s.remoteMu.Unlock()
		log.Printf("Приёмник: вход с телефона — одноразовые коды выключены")
	case "logout-all":
		s.sess.clear()
		log.Printf("Приёмник: выполнен выход на всех телефонах")
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.remoteInfo(r))
}

func onOff(on bool) string {
	if on {
		return "включена"
	}
	return "выключена"
}

// ---------- the page in the tailnet: https://name.tailnet.ts.net ----------

// startTSWeb serves the page on ports 443 (a real certificate from
// Tailscale, when HTTPS is enabled for the tailnet) and 80 of the node.
// The agent port of the node serves it too (see agentPortHandler).
func (s *Server) startTSWeb() {
	s.tsMu.Lock()
	n := s.ts
	s.tsMu.Unlock()
	if n == nil || !s.remoteOn("tailscale") {
		return
	}
	s.remoteMu.Lock()
	if s.tsWebStop != nil {
		s.remoteMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.tsWebStop = cancel
	s.remoteMu.Unlock()
	go s.runTSWeb(ctx, n)
}

func (s *Server) stopTSWeb() {
	s.remoteMu.Lock()
	stop := s.tsWebStop
	s.tsWebStop, s.tsWebURL, s.tsWebErr = nil, "", ""
	s.remoteMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *Server) runTSWeb(ctx context.Context, n *tsnode.Node) {
	page := s.remoteHandler()
	var servers []*http.Server
	serve := func(ln net.Listener, h http.Handler) {
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				log.Printf("Приёмник: страница в Tailscale: %v", err)
			}
		}()
	}
	defer func() {
		for _, srv := range servers {
			srv.Close()
		}
	}()
	setState := func(url, errText string) {
		s.remoteMu.Lock()
		if s.tsWebStop != nil {
			s.tsWebURL, s.tsWebErr = url, errText
		}
		s.remoteMu.Unlock()
	}
	// port 80: the page itself until the certificate works, then a redirect
	var httpsURL string
	var urlMu sync.Mutex
	if ln, err := n.Listen(":80"); err == nil {
		serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			urlMu.Lock()
			u := httpsURL
			urlMu.Unlock()
			if u != "" {
				http.Redirect(w, r, strings.TrimSuffix(u, "/")+r.URL.RequestURI(), http.StatusFound)
				return
			}
			page.ServeHTTP(w, r)
		}))
	} else {
		log.Printf("Приёмник: Tailscale, порт 80: %v", err)
	}
	var tlsDomain string
	for {
		wait := 5 * time.Second
		if n.Status().State == tsnode.StateRunning {
			wait = 2 * time.Minute // on failure, e.g. HTTPS not enabled yet: check again later
			if tlsDomain == "" {
				ln, domain, err := n.ListenTLS(":443")
				if err != nil {
					setState("", err.Error())
				} else {
					serve(ln, page)
					tlsDomain = domain
				}
			}
			if tlsDomain != "" {
				// port 80 redirects to https only once the certificate is really there
				cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
				err := n.CheckCert(cctx, tlsDomain)
				cancel()
				if err == nil {
					u := "https://" + tlsDomain + "/"
					urlMu.Lock()
					httpsURL = u
					urlMu.Unlock()
					setState(u, "")
					log.Printf("Приёмник: страница в Tailscale: %s", u)
					<-ctx.Done()
					return
				}
				if ctx.Err() != nil {
					return
				}
				setState("", "Tailscale не выдал сертификат HTTPS: "+err.Error())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
