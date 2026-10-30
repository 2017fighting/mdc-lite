//go:build acceptance

package acceptance

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the driver that reads the server's library database
)

// jellyfin is the media server side of the acceptance environment. It is a thin
// client on purpose: the wizard, the library and the scan trigger are the same HTTP
// calls the web interface makes, and doing them from here is what makes the run
// reproducible — a hand-clicked server is not an acceptance environment.
type jellyfin struct {
	base  string
	http  *http.Client
	token string
}

// jellyfinBase is the server's address. It defaults to the port `compose.yaml`
// publishes and can be pointed elsewhere (a manual server, a different port).
func jellyfinBase() string {
	if v := strings.TrimSpace(getenv("MDC_ACCEPT_JELLYFIN_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:" + orDefault(getenv("MDC_ACCEPT_JELLYFIN_PORT"), "8096")
}

func newJellyfin() *jellyfin {
	return &jellyfin{base: jellyfinBase(), http: &http.Client{Timeout: 120 * time.Second}}
}

// item is what the assertions read off the server. Only the fields the environment
// speaks about are decoded: a wider struct would become a claim about the server's
// API that this environment would then have to maintain.
type item struct {
	ID               string            `json:"Id"`
	Name             string            `json:"Name"`
	Path             string            `json:"Path"`
	PremiereDate     string            `json:"PremiereDate"`
	ProductionYear   int               `json:"ProductionYear"`
	IsInMixedFolder  bool              `json:"IsInMixedFolder"`
	MediaSources     []mediaSource     `json:"MediaSources"`
	ImageTags        map[string]string `json:"ImageTags"`
	BackdropImageTag []string          `json:"BackdropImageTags"`
	Overview         string
	RunTimeTicks     int64
	CommunityRating  float64
	Studio           string
	OfficialRating   string
}

type mediaSource struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
	Path string `json:"Path"`
	Size int64  `json:"Size"`
}

// setup completes the first-run wizard and returns the access token.
//
// GET /Startup/FirstUser comes before the POST for a reason measured on 10.10.x: on
// a fresh database Jellyfin has zero users, the POST opens with "take the first
// user", and the GET is what seeds the default one. A script that skips it gets a
// 500 and then a 401 from the login that follows.
func (j *jellyfin) setup(t *testing.T) {
	t.Helper()

	// Idempotent: a second run over the same scratch root must not re-run a wizard
	// that has already run, and a token is what proves it has.
	if reply := j.do(t, http.MethodPost, "/Users/AuthenticateByName", authBody(), ""); reply.status == http.StatusOK {
		j.token = tokenOf(t, reply.body)
		t.Logf("jellyfin: already set up, logged in")
		return
	}

	j.do(t, http.MethodGet, "/Startup/Configuration", nil, "").expectOK(t, "read the wizard's configuration")
	j.do(t, http.MethodGet, "/Startup/FirstUser", nil, "").expectOK(t,
		"GET /Startup/FirstUser (this call is what seeds the account the wizard's POST needs)")
	j.do(t, http.MethodPost, "/Startup/User", map[string]any{"Name": user, "Password": password}, "").
		expectWrite(t, "create the administrator")
	j.do(t, http.MethodPost, "/Startup/Configuration", map[string]any{
		"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"}, "").
		expectWrite(t, "set the wizard's configuration")
	j.do(t, http.MethodPost, "/Startup/RemoteAccess", map[string]any{
		"EnableRemoteAccess": false, "EnableAutomaticPortMapping": false}, "").
		expectWrite(t, "answer the remote-access step")
	j.do(t, http.MethodPost, "/Startup/Complete", map[string]any{}, "").
		expectWrite(t, "complete the wizard")

	reply := j.do(t, http.MethodPost, "/Users/AuthenticateByName", authBody(), "")
	reply.expectOK(t, "log in after the wizard")
	j.token = tokenOf(t, reply.body)
	t.Logf("jellyfin: wizard complete")
}

// ensureLibrary points the server at the library root the product writes into.
//
// The options are the assertions' foundation: local NFO reading on, every online
// fetcher off. A server left to its own devices fetches its own metadata over the
// NFO, and the run would then be measuring the internet rather than what the
// pipeline wrote.
func (j *jellyfin) ensureLibrary(t *testing.T, path string) {
	t.Helper()
	reply := j.do(t, http.MethodGet, "/Library/VirtualFolders", nil, j.token)
	reply.expectOK(t, "list the libraries")
	for _, folder := range decodeList(t, reply.body) {
		if folderPathIs(folder, path) {
			t.Logf("jellyfin: a library already covers %s", path)
			return
		}
	}
	j.do(t, http.MethodPost,
		"/Library/VirtualFolders?name="+url.QueryEscape(libraryName)+"&collectionType=movies&refreshLibrary=false",
		map[string]any{"LibraryOptions": map[string]any{
			"PathInfos":               []any{map[string]any{"Path": path}},
			"EnableRealtimeMonitor":   false,
			"EnableInternetProviders": false,
			"MetadataReaders":         []string{"Nfo"},
			"MetadataFetchers":        []string{},
			"ImageFetchers":           []string{},
			"SubtitleFetchers":        []string{},
			"SaveLocalMetadata":       false,
			"EnableLUFSScan":          false,
		}}, j.token).expectWrite(t, "create the library")
	t.Logf("jellyfin: library %q now covers %s", libraryName, path)
}

// scan makes the media server look at the library again.
//
// Two calls, because they answer two different questions and the run needs both:
//
//   - `POST /Library/Media/Updated` is the path-scoped update the product's own
//     library refresh would send (batch two). It re-reads paths the library already
//     knows, and it answers 204 even for a path that does not exist, so it can never
//     be the acceptance run's proof of anything on its own;
//   - the server's own "Scan Media Library" task is what discovers a directory that
//     appeared since the last scan. The acceptance environment triggers that itself
//     because the product does not refresh libraries in V1, and "somebody clicked
//     refresh" is not a reproducible step.
//
// What the run then waits for is the items it expects, never the scan: a scan that
// finished is not the same thing as a server that read the files.
func (j *jellyfin) scan(t *testing.T, paths ...string) {
	t.Helper()
	updates := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		updates = append(updates, map[string]string{"Path": p, "UpdateType": "Modified"})
	}
	if len(updates) > 0 {
		j.do(t, http.MethodPost, "/Library/Media/Updated", map[string]any{"Updates": updates}, j.token).
			expectWrite(t, "send a path-scoped library update")
	}
	j.do(t, http.MethodPost, "/ScheduledTasks/Running/"+j.scanTaskID(t), nil, j.token).
		expectWrite(t, "start the library scan")
	t.Logf("jellyfin: library scan triggered (%s)", strings.Join(paths, ", "))
}

// version is the server's own version string, read from the one endpoint that answers
// before the wizard has run.
func (j *jellyfin) version(t *testing.T) string {
	t.Helper()
	info := j.publicInfo(t)
	if info == nil {
		t.Fatal("jellyfin: /System/Info/Public does not answer, so the pinned version cannot be checked")
	}
	version, _ := info["Version"].(string)
	if version == "" {
		t.Fatalf("jellyfin: /System/Info/Public carries no Version: %v", info)
	}
	return version
}

// scanTaskID finds the server's library-scan task, by key rather than by name: the
// name is translated, the key is not.
func (j *jellyfin) scanTaskID(t *testing.T) string {
	t.Helper()
	reply := j.do(t, http.MethodGet, "/ScheduledTasks", nil, j.token)
	reply.expectOK(t, "list the scheduled tasks")
	for _, task := range decodeList(t, reply.body) {
		if task["Key"] == "RefreshLibrary" {
			id, _ := task["Id"].(string)
			if id == "" {
				t.Fatalf("jellyfin: the library-scan task has no id: %v", task)
			}
			return id
		}
	}
	t.Fatal("jellyfin: this server has no RefreshLibrary task; the scan trigger would be a no-op")
	return ""
}

// movies asks for every movie the library holds. The assertions then look for the
// entries they expect; nothing counts them, because the library also holds the
// primer this environment planted and whatever else the server decided to keep.
func (j *jellyfin) movies(t *testing.T) []item {
	t.Helper()
	q := url.Values{
		"Recursive":        {"true"},
		"IncludeItemTypes": {"Movie"},
		"Fields":           {"Path,MediaSources,PremiereDate,Overview,RunTimeTicks,CommunityRating,Studio,ImageTags,BackdropImageTags"},
		"Limit":            {"500"},
	}
	reply := j.do(t, http.MethodGet, "/Items?"+q.Encode(), nil, j.token)
	reply.expectOK(t, "list the library's movies")
	var page struct {
		Items []item `json:"Items"`
	}
	if err := json.Unmarshal(reply.body, &page); err != nil {
		t.Fatalf("jellyfin: decode the movie list: %v (%s)", err, summarize(reply.body))
	}
	return page.Items
}

// additionalParts reads the second disc of a multi-disc release.
//
// Jellyfin keeps the extra discs of one release as `AdditionalParts` of the single
// entry rather than as extra media sources or extra entries: the acceptance run
// asserts that the entry carries the second disc, because "two files became one
// movie" is the claim, and the way the server spells it is its own business.
func (j *jellyfin) additionalParts(t *testing.T, id string) []item {
	t.Helper()
	reply := j.do(t, http.MethodGet, "/Videos/"+id+"/AdditionalParts", nil, j.token)
	reply.expectOK(t, "read a release's additional parts")
	var page struct {
		Items []item `json:"Items"`
	}
	if err := json.Unmarshal(reply.body, &page); err != nil {
		t.Fatalf("jellyfin: decode the additional parts: %v (%s)", err, summarize(reply.body))
	}
	return page.Items
}

// mixedFolder reads one item's IsInMixedFolder out of the server's own library
// database.
//
// The API answers the field only when it is true — a false one is omitted — so the
// API cannot tell "not in a mixed folder" from "the server did not say", and an
// assertion on it through the API would be unsatisfiable rather than strict. The
// database is read read-only, after the scan, from the host: the scratch root is
// mounted at the same absolute path inside the container, which is exactly what makes
// this the same file the server has open.
func (j *jellyfin) mixedFolder(t *testing.T, dbPath, itemPath string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatalf("jellyfin: open the library database %s read-only: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	var mixed bool
	err = db.QueryRow(`SELECT IsInMixedFolder FROM TypedBaseItems WHERE Path = ?`, itemPath).Scan(&mixed)
	if err != nil {
		t.Fatalf("jellyfin: read IsInMixedFolder for %s: %v", itemPath, err)
	}
	return mixed
}

// clientAuthorization identifies this client to the server the way the web interface
// does. It is a constant: there is one acceptance client.
const clientAuthorization = `MediaBrowser Client="mdc-lite-acceptance", Device="acceptance", DeviceId="mdc-lite-acceptance-1", Version="1.0"`

// ---- transport ----

// okWrites are the two statuses Jellyfin uses for a write that worked: some of its
// POSTs answer 204 with an empty body, some answer 200 with a body.
var okWrites = []int{http.StatusOK, http.StatusNoContent}

// reply is one answer. Nothing here retries: every step is idempotent and the caller
// decides what a failure means, which is what keeps a green run to one attempt.
type reply struct {
	status int
	body   []byte
}

func (r reply) expectOK(t *testing.T, what string) {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("jellyfin: %s: got HTTP %d, want 200: %s", what, r.status, summarize(r.body))
	}
}

func (r reply) expectWrite(t *testing.T, what string) {
	t.Helper()
	for _, w := range okWrites {
		if r.status == w {
			return
		}
	}
	t.Fatalf("jellyfin: %s: got HTTP %d, want one of %v: %s", what, r.status, okWrites, summarize(r.body))
}

func (j *jellyfin) do(t *testing.T, method, path string, body any, token string) reply {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("jellyfin: encode the request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, j.base+path, reader)
	if err != nil {
		t.Fatalf("jellyfin: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", clientAuthorization)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := j.http.Do(req)
	if err != nil {
		t.Fatalf("jellyfin: %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: raw}
}

// publicInfo is the one endpoint that answers before the wizard has run, so it is
// also the readiness probe.
func (j *jellyfin) publicInfo(t *testing.T) map[string]any {
	t.Helper()
	resp, err := j.http.Get(j.base + "/System/Info/Public")
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func authBody() map[string]any {
	return map[string]any{"Username": user, "Pw": password}
}

func tokenOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("jellyfin: decode the login answer: %v (%s)", err, summarize(body))
	}
	if out.AccessToken == "" {
		t.Fatalf("jellyfin: logged in but the answer carried no access token: %s", summarize(body))
	}
	return out.AccessToken
}

func decodeList(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("jellyfin: decode a list: %v (%s)", err, summarize(body))
	}
	return out
}

func folderPathIs(folder map[string]any, path string) bool {
	opts, ok := folder["LibraryOptions"].(map[string]any)
	if !ok {
		return false
	}
	paths, ok := opts["PathInfos"].([]any)
	if !ok {
		return false
	}
	for _, p := range paths {
		if m, ok := p.(map[string]any); ok && m["Path"] == path {
			return true
		}
	}
	return false
}

func summarize(body []byte) string {
	const max = 400
	s := strings.TrimSpace(string(body))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
