//go:build authelia

// Contract test against a real Authelia. Run with:
//
//	go test -tags authelia ./internal/auth
//
// It needs a Docker-compatible socket and both images below already
// present locally; it never pulls images.
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"chatchat/internal/store"
)

// The hashed client secret and passwords in testdata were generated for the
// homepage project, so the plaintext test values below keep that name.
const (
	autheliaImage = "authelia/authelia:4.39.20"
	// ryukImage is the reaper testcontainers-go v0.44.0 starts to remove
	// containers even if the test process is killed. Keep it in step with
	// ReaperDefaultImage when upgrading testcontainers-go.
	ryukImage = "testcontainers/ryuk:0.14.0"
	authHost  = "auth.chatchat.test"
	publicURL = "https://chatchat.test"
	password  = "homepage-test-password"
)

var (
	//go:embed testdata/authelia/configuration.yml
	autheliaConfig string
	//go:embed testdata/authelia/users.yml
	autheliaUsers []byte
)

func TestAutheliaLoginLogout(t *testing.T) {
	issuer, client := startAuthelia(t)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, Config{
		PublicURL:    publicURL,
		Issuer:       issuer,
		ClientID:     "chatchat",
		ClientSecret: "homepage-test-client-secret",
		HTTPClient:   client,
	})
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.Handle("GET /whoami", whoami(svc))
	app := &browser{t: t, handler: mux, idp: client, issuer: issuer}

	alice := app.login("alice")
	first := app.whoami(alice)
	if first["name"] != "Alice Test" || first["email"] != "alice@example.com" {
		t.Fatalf("alice = %v", first)
	}

	// A second login (another browser) maps to the same user.
	again := app.login("alice")
	if got := app.whoami(again); got["id"] != first["id"] {
		t.Fatalf("relogin id = %v, want %v", got["id"], first["id"])
	}

	bob := app.whoami(app.login("bob"))
	if bob["id"] == first["id"] || bob["name"] != "Bob Test" {
		t.Fatalf("bob = %v, alice = %v", bob, first)
	}

	// Logging out ends that browser's session only.
	if code := app.call("POST", "/auth/logout", alice).Code; code != http.StatusNoContent {
		t.Fatalf("logout: status = %d", code)
	}
	if code := app.call("GET", "/whoami", alice).Code; code != http.StatusUnauthorized {
		t.Fatalf("after logout: status = %d", code)
	}
	if got := app.whoami(again); got["id"] != first["id"] {
		t.Fatalf("other browser lost its session: %v", got)
	}
}

func TestAutheliaCallbackRejectsForeignState(t *testing.T) {
	issuer, client := startAuthelia(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, Config{
		PublicURL: publicURL, Issuer: issuer, ClientID: "chatchat",
		ClientSecret: "homepage-test-client-secret", HTTPClient: client,
	})
	mux := http.NewServeMux()
	svc.Register(mux)
	app := &browser{t: t, handler: mux, idp: client, issuer: issuer}

	// Alice's browser starts a login; the code is replayed into a browser
	// that started its own, different login.
	code, _ := app.authorize("alice")
	_, victim := app.startLogin()
	rec := app.call("GET", "/auth/callback?code="+url.QueryEscape(code)+"&state=forged", victim)
	if rec.Code != http.StatusBadRequest || sessionFrom(rec) != "" {
		t.Fatalf("status = %d, session = %q", rec.Code, sessionFrom(rec))
	}
}

type browser struct {
	t       *testing.T
	handler http.Handler
	idp     *http.Client
	issuer  string
}

// login completes the whole flow and returns the session cookie value.
func (b *browser) login(user string) string {
	b.t.Helper()
	code, loginCookie := b.authorize(user)
	rec := b.call("GET", "/auth/callback?"+code, loginCookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		b.t.Fatalf("callback: status = %d, body = %s", rec.Code, rec.Body)
	}
	session := sessionFrom(rec)
	if session == "" {
		b.t.Fatal("callback set no session cookie")
	}
	return session
}

// startLogin hits /auth/login and returns the provider URL and the login
// cookie as a Cookie header value.
func (b *browser) startLogin() (string, string) {
	b.t.Helper()
	rec := b.call("GET", "/auth/login", "")
	if rec.Code != http.StatusFound {
		b.t.Fatalf("login: status = %d, body = %s", rec.Code, rec.Body)
	}
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == loginCookie {
			cookie = c.Name + "=" + c.Value
		}
	}
	return rec.Header().Get("Location"), cookie
}

// authorize signs user in at Authelia and returns the callback query
// (code and state) together with the login cookie it belongs to.
func (b *browser) authorize(user string) (string, string) {
	b.t.Helper()
	authURL, loginCookie := b.startLogin()

	body, _ := json.Marshal(map[string]any{
		"username": user, "password": password, "keepMeLoggedIn": false,
		"targetURL": authURL, "requestMethod": "GET",
	})
	req, _ := http.NewRequest("POST", b.issuer+"/api/firstfactor", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", b.issuer)
	res := b.do(req)
	if res.StatusCode != http.StatusOK {
		b.t.Fatalf("first factor: %d %s", res.StatusCode, readAll(res))
	}
	var idpCookies []string
	for _, c := range res.Cookies() {
		idpCookies = append(idpCookies, c.Name+"="+c.Value)
	}

	req, _ = http.NewRequest("GET", authURL, nil)
	req.Header.Set("Cookie", strings.Join(idpCookies, "; "))
	res = b.do(req)
	if res.StatusCode != http.StatusFound && res.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("authorize: %d %s", res.StatusCode, readAll(res))
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || loc.Host != "chatchat.test" || loc.Query().Get("code") == "" {
		b.t.Fatalf("authorize redirected to %q", res.Header.Get("Location"))
	}
	return loc.RawQuery, loginCookie
}

func (b *browser) whoami(session string) map[string]any {
	b.t.Helper()
	rec := b.call("GET", "/whoami", session)
	if rec.Code != http.StatusOK {
		b.t.Fatalf("whoami: status = %d, body = %s", rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// call sends a request to the chatchat handler. cookie is either a bare
// session token or a full "name=value" Cookie header.
func (b *browser) call(method, path, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, publicURL+path, nil)
	switch {
	case cookie == "":
	case strings.Contains(cookie, "="):
		req.Header.Set("Cookie", cookie)
	default:
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	b.handler.ServeHTTP(rec, req)
	return rec
}

func (b *browser) do(req *http.Request) *http.Response {
	b.t.Helper()
	res, err := b.idp.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { res.Body.Close() })
	return res
}

func sessionFrom(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge > 0 {
			return c.Value
		}
	}
	return ""
}

func readAll(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// startAuthelia runs Authelia with a freshly generated TLS certificate and
// signing key and returns its issuer URL and a client that trusts it.
func startAuthelia(t *testing.T) (string, *http.Client) {
	t.Helper()
	// testcontainers pulls missing images on its own; fail first instead.
	for _, image := range []string{autheliaImage, ryukImage} {
		if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
			t.Fatalf("image %s must be present locally (no pulls): %v", image, err)
		}
	}

	// The issuer URL must be the same inside and outside the container, so
	// the host port is fixed to a free port chosen up front.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	caPEM, certPEM, keyPEM := tlsFiles(t)
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signerPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(signer)})
	var indented []string
	for _, line := range strings.Split(strings.TrimSpace(string(signerPEM)), "\n") {
		indented = append(indented, "          "+line)
	}
	config := strings.NewReplacer(
		"@PORT@", strconv.Itoa(port),
		"@SIGNING_KEY@", strings.Join(indented, "\n"),
	).Replace(autheliaConfig)

	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        autheliaImage,
			ExposedPorts: []string{"9091/tcp"},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.PortBindings = network.PortMap{
					network.MustParsePort("9091/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(port)}},
				}
			},
			Files: []testcontainers.ContainerFile{
				{Reader: strings.NewReader(config), ContainerFilePath: "/config/configuration.yml", FileMode: 0o644},
				{Reader: bytes.NewReader(autheliaUsers), ContainerFilePath: "/config/users.yml", FileMode: 0o644},
				{Reader: bytes.NewReader(certPEM), ContainerFilePath: "/config/tls-cert.pem", FileMode: 0o644},
				{Reader: bytes.NewReader(keyPEM), ContainerFilePath: "/config/tls-key.pem", FileMode: 0o644},
			},
			WaitingFor: wait.ForLog("Listening for TLS connections").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if container != nil {
		t.Cleanup(func() { testcontainers.TerminateContainer(container) })
	}
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots},
			// Resolve the fixture host name to the published port.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if host, _, _ := net.SplitHostPort(addr); host == authHost {
					addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return fmt.Sprintf("https://%s:%d", authHost, port), client
}

// tlsFiles returns a throwaway CA and a server certificate for authHost
// signed by it.
func tlsFiles(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "chatchat test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: authHost},
		DNSNames:     []string{authHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
