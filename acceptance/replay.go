//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"image"
	"image/color"
	"image/jpeg"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/fixtures"
)

// replayProxy is the boundary the acceptance environment replaces: every request
// the product makes — a metadata page, an image, any source — arrives here instead
// of at the site, and is answered from the committed captures.
//
// It is an HTTP proxy rather than a rewritten base URL because the images cannot be
// rewritten: a captured page names its cover with an absolute `https://` address,
// the product resolves that address off the page it is reading, and a page whose
// bytes had been edited to point somewhere else would no longer be the page the
// site sent. So the replay sits where the network is, which is also the only place
// that sees every request the product makes.
//
// HTTPS through a proxy is a CONNECT tunnel, so this type terminates the TLS
// itself with a certificate minted from a CA generated for this run; the child
// process trusts that CA through SSL_CERT_FILE. Nothing outside this environment
// ever sees the key.
type replayProxy struct {
	// URL is what `config.yaml`'s `proxy:` is set to.
	URL string
	// CAPath is the PEM file the child process gets in SSL_CERT_FILE.
	CAPath string

	server *httptest.Server

	mu      sync.Mutex
	pages   map[string]page
	images  map[string][]byte
	seen    []request
	scripts []routeScript

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	leafs  map[string]*tls.Certificate
}

type page struct {
	status int
	body   []byte
}

// routeScript is one deliberate answer that replaces what the captures hold. The
// acceptance run uses it to make the network answer the way a real site sometimes
// does — too slowly, with a 5xx, with a refusal, with the connection gone — because
// the six reason codes are the product's reading of an answer, and a simulated
// branch inside the product would prove only that the branch is reachable.
//
// The match is a predicate rather than a route key because two of the scripts have to
// recognise the source's own query spelling (javdb's search carries the number in its
// query string), and pinning that spelling in the test would make the test the second
// copy of the source's URL shape.
type routeScript struct {
	name  string
	match func(host, path, query string) bool
	// status answers instead of the capture (`0` falls through to the capture).
	status int
	body   []byte
	// delay holds the answer back so the product's own per-request timeout fires;
	// `request.Context()` ends it, so a timed-out request does not hold the replay's
	// shutdown open.
	delay time.Duration
	// hangup closes the connection before answering, which is the transport-shaped
	// failure (`egress_unavailable`) rather than an HTTP answer.
	hangup bool
}

// request is one answered request, kept so a test can assert what the product
// actually asked for rather than what it was expected to ask for.
type request struct {
	Method string
	Host   string
	Path   string
	Key    string
	Status int
	// Scripted is the name of the deliberate answer that replaced the capture, empty
	// when the request was answered by one. The image assertion reads it: an image
	// that did not arrive has to be a script, never a missing capture.
	Scripted string
}

// imageExts are the extensions a replay answers as an image. A captured page's
// image references are the only addresses that ever arrive here, and they all name
// a file; anything else is a metadata request, and inventing an answer for it would
// hide a page the fixtures do not have.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// newReplay builds the proxy over the given capture directories. Constructed
// fixtures have no address to replay, so they are left out: they exist for the
// parser, which reaches them through the unit tests, not for the wire.
func newReplay(t *testing.T, caFile string, dirs ...string) *replayProxy {
	t.Helper()
	r := &replayProxy{
		pages:  map[string]page{},
		images: map[string][]byte{},
		leafs:  map[string]*tls.Certificate{},
	}
	for _, dir := range dirs {
		rows, err := fixtures.Load(dir)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		for _, problem := range fixtures.Problems(dir) {
			t.Fatalf("replay: a capture directory does not hold up: %s", problem)
		}
		for _, row := range rows {
			if row.URL == "" {
				continue // a constructed fixture has no address to stand in for
			}
			u, err := url.Parse(row.URL)
			if err != nil {
				t.Fatalf("replay: %s: %v", row.Path, err)
			}
			body, err := os.ReadFile(filepath.Join(dir, row.Path))
			if err != nil {
				t.Fatalf("replay: read %s: %v", row.Path, err)
			}
			r.pages[routeKey(u)] = page{status: row.Status, body: body}
		}
	}
	if err := r.mintCAPool(caFile); err != nil {
		t.Fatalf("replay: %v", err)
	}
	r.server = httptest.NewServer(r)
	t.Cleanup(r.close)
	r.URL = r.server.URL
	return r
}

// Requests returns what has been served so far, oldest first.
func (r *replayProxy) Requests() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.seen...)
}

// ScriptStatus answers a matching request with a status instead of its capture. It is
// how the reason codes that start with a status code (`http_5xx`, `blocked`,
// `cooldown`) are produced for real.
func (r *replayProxy) ScriptStatus(t *testing.T, name string, match func(host, path, query string) bool, status int) {
	t.Helper()
	r.script(t, routeScript{name: name, match: match, status: status, body: []byte(name + "\n")})
}

// ScriptBody answers with these bytes, so a source that expects JSON can be given a
// page instead: that is the `parse` code, and the bytes are a captured page rather
// than an invented string.
func (r *replayProxy) ScriptBody(t *testing.T, name string, match func(host, path, query string) bool, status int, body []byte) {
	t.Helper()
	r.script(t, routeScript{name: name, match: match, status: status, body: body})
}

// ScriptDelay holds the capture back for d, so the product's per-request timeout is
// the thing that decides the outcome.
func (r *replayProxy) ScriptDelay(t *testing.T, name string, match func(host, path, query string) bool, d time.Duration) {
	t.Helper()
	r.script(t, routeScript{name: name, match: match, delay: d})
}

// ScriptHangUp closes the connection instead of answering, which is what a dead exit
// looks like from the client: not a status, not a 404 — no answer at all.
func (r *replayProxy) ScriptHangUp(t *testing.T, name string, match func(host, path, query string) bool) {
	t.Helper()
	r.script(t, routeScript{name: name, match: match, hangup: true})
}

// ClearScripts drops every script, so an arm that made the network fail cannot leak
// into the next one.
func (r *replayProxy) ClearScripts() {
	r.mu.Lock()
	r.scripts = nil
	r.mu.Unlock()
}

func (r *replayProxy) script(t *testing.T, sc routeScript) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.scripts {
		if existing.name == sc.name {
			t.Fatalf("replay: the script %q already exists", sc.name)
		}
	}
	r.scripts = append(r.scripts, sc)
}

// scriptFor finds the deliberate answer for one request, if there is one.
func (r *replayProxy) scriptFor(host, path, query string) (routeScript, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sc := range r.scripts {
		if sc.match(host, path, query) {
			return sc, true
		}
	}
	return routeScript{}, false
}

func (r *replayProxy) close() {
	if r.server != nil {
		r.server.Close()
	}
}

// ServeHTTP answers a proxied request. A CONNECT is a tunnel to establish; anything
// else is a plain HTTP request that carries its own absolute URL.
func (r *replayProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		r.tunnel(w, req)
		return
	}
	r.serve(w, req, req.Host)
}

// tunnel opens the TLS mitm for one CONNECT: answer 200, then speak TLS with a
// certificate for the requested host, and serve the decrypted requests from there.
func (r *replayProxy) tunnel(w http.ResponseWriter, req *http.Request) {
	host, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		host = req.Host
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "the replay cannot hijack this connection", http.StatusInternalServerError)
		return
	}
	cert, err := r.certFor(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	tlsConn := tls.Server(conn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	})
	// One connection, one host: the http.Server loop ends when the client does.
	// The loop ends when the client closes the tunnel; a torn connection mid-request is
	// the client's business, not a failure of the replay.
	_ = (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.serve(w, req, host)
	})}).Serve(&oneConnListener{conn: tlsConn})
}

// serve answers one request from the captures, or from the generated images.
func (r *replayProxy) serve(w http.ResponseWriter, req *http.Request, host string) {
	key := routeKeyOf(host, req.URL.Path, req.URL.RawQuery)
	sc, scripted := r.scriptFor(host, req.URL.Path, req.URL.RawQuery)
	if scripted {
		if sc.hangup {
			// Torn down mid-request: the client sees no answer instead of a status.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					r.record(request{Method: req.Method, Host: host, Path: req.URL.Path, Key: key, Status: 0, Scripted: sc.name})
					return
				}
			}
			r.record(request{Method: req.Method, Host: host, Path: req.URL.Path, Key: key, Status: 0, Scripted: sc.name})
			return
		}
		if sc.delay > 0 {
			select {
			case <-time.After(sc.delay):
			case <-req.Context().Done():
				// The product gave up first; which is the point of the delay.
				r.record(request{Method: req.Method, Host: host, Path: req.URL.Path, Key: key, Status: 0, Scripted: sc.name})
				return
			}
		}
		if sc.status != 0 {
			w.WriteHeader(sc.status)
			_, _ = w.Write(sc.body)
			r.record(request{Method: req.Method, Host: host, Path: req.URL.Path, Key: key, Status: sc.status, Scripted: sc.name})
			return
		}
	}

	r.mu.Lock()
	pg, ok := r.pages[key]
	img, isImage := r.images[key]
	r.mu.Unlock()

	status := http.StatusNotFound
	switch {
	case ok:
		status = pg.status
		w.WriteHeader(pg.status)
		_, _ = w.Write(pg.body)
	case imageExts[strings.ToLower(filepath.Ext(req.URL.Path))]:
		// The product's image fields are only contributions once the bytes are in
		// hand, so a replay that answered 404 for covers would make every task
		// partial for a reason that has nothing to do with the code under test.
		if !isImage {
			img = renderedImage(key)
			r.mu.Lock()
			r.images[key] = img
			r.mu.Unlock()
		}
		status = http.StatusOK
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(img)
	default:
		// No invented answer: a number without a capture is a 404 here, exactly as it
		// can be on the site. That is what makes a missing fixture visible.
		http.Error(w, "no capture for "+key, http.StatusNotFound)
	}
	r.record(request{Method: req.Method, Host: host, Path: req.URL.Path, Key: key, Status: status, Scripted: scriptName(sc, scripted)})
}

// record keeps one answered request, so a test can assert what the product actually
// asked for instead of what it was expected to ask for.
func (r *replayProxy) record(req request) {
	r.mu.Lock()
	r.seen = append(r.seen, req)
	r.mu.Unlock()
}

func scriptName(sc routeScript, scripted bool) string {
	if !scripted {
		return ""
	}
	return sc.name
}

// routeKey is the address a capture stands in for, with `www.` dropped so the site's
// own spelling (`javbus.com` → `www.javbus.com`) still matches.
func routeKey(u *url.URL) string {
	return routeKeyOf(u.Host, u.Path, u.RawQuery)
}

func routeKeyOf(host, path, rawQuery string) string {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "www.")
	key := host + path
	if rawQuery != "" {
		key += "?" + rawQuery
	}
	return key
}

// renderedImage makes one deterministic 16:9 JPEG for an address. It is generated
// rather than captured because an image is not a page a parser reads: what the
// pipeline does with the bytes (sniff the type, place poster/fanart/extrafanart)
// and what the media server does with them is the thing under test.
func renderedImage(key string) []byte {
	const w, h = 960, 540
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// A colour derived from the address, so two stills of one release are
	// distinguishable and a crop is visibly a crop.
	seed := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		seed = (seed ^ uint32(key[i])) * 16777619
	}
	band := color.RGBA{R: uint8(seed >> 16), G: uint8(seed >> 8), B: uint8(seed), A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			shade := uint8(40 + 200*x/w)
			img.Set(x, y, color.RGBA{
				R: uint8((uint16(band.R)*uint16(shade))/255 + 20),
				G: uint8((uint16(band.G)*uint16(shade))/255 + 20),
				B: uint8((uint16(band.B)*uint16(shade))/255 + 20),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		panic("replay: encode the generated image: " + err.Error())
	}
	return buf.Bytes()
}

// mintCAPool generates the run's CA and writes its certificate where the child
// process can find it. The key never leaves this process.
func (r *replayProxy) mintCAPool(path string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mdc-lite acceptance replay CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	r.caCert = cert
	r.caKey = key
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		return err
	}
	r.CAPath = path
	return nil
}

// certFor mints (once) a leaf certificate for one host, signed by the run's CA.
func (r *replayProxy) certFor(host string) (*tls.Certificate, error) {
	r.mu.Lock()
	cached, ok := r.leafs[host]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, r.caCert, &key.PublicKey, r.caKey)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	r.mu.Lock()
	r.leafs[host] = &cert
	r.mu.Unlock()
	return &cert, nil
}

// oneConnListener is the one-connection listener http.Serve needs for a hijacked
// tunnel. The first Accept hands over the TLS connection, the second reports the
// listener closed, which ends the serving loop with no error.
type oneConnListener struct {
	conn net.Conn
	done bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if l.done {
		return nil, net.ErrClosed
	}
	l.done = true
	return l.conn, nil
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// compile-time check that the proxy is an http.Handler.
var _ http.Handler = (*replayProxy)(nil)
