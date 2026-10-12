package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryStore map[string]string

func (s memoryStore) Get(k string) (string, error) {
	v, ok := s[k]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (s memoryStore) Set(k, v string) error { s[k] = v; return nil }
func (s memoryStore) Delete(k string) error { delete(s, k); return nil }

func TestOrigin(t *testing.T) {
	for _, raw := range []string{"https://example.org", "http://localhost:3000", "http://127.0.0.1:3100", "http://[::1]:3000"} {
		if _, err := validateOrigin(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.org", "https://u:p@example.org", "https://example.org/path", "https://example.org?q=x", "ftp://localhost", "http://127.0.0.1.example.org"} {
		if _, err := validateOrigin(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestCrossOriginRedirectDoesNotLeakToken(t *testing.T) {
	called := false
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer evil.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, evil.URL, 302) }))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err := secureHTTP().Do(req)
	if res != nil {
		res.Body.Close()
	}
	if err == nil || called {
		t.Fatal("cross-origin redirect was followed")
	}
}

func TestCallbackRejectsWrongStateAndAcceptsOnce(t *testing.T) {
	codes := make(chan string, 2)
	denied := make(chan error, 2)
	h := callbackHandler("correct", codes, denied)
	for _, target := range []string{"/callback?state=wrong&code=attacker", "/other?state=correct&code=attacker", "/callback?state=correct"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code < 400 {
			t.Fatal("invalid callback accepted")
		}
	}
	if len(codes) != 0 {
		t.Fatal("invalid callback completed login")
	}
	for _, code := range []string{"first", "second"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/callback?state=correct&code="+code, nil))
	}
	if len(codes) != 1 || <-codes != "first" {
		t.Fatal("callback did not deliver only the first valid code")
	}
	// RFC 7636 example verifies exact S256 encoding.
	if pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal("invalid PKCE challenge")
	}
}

func TestCallbackDenial(t *testing.T) {
	codes := make(chan string, 1)
	denied := make(chan error, 1)
	h := callbackHandler("ok", codes, denied)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/callback?state=ok&error=access_denied", nil))
	if len(denied) != 1 || len(codes) != 0 {
		t.Fatal("denial not handled")
	}
}

func TestRefreshRotatesAndStoresCredentials(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/auth/v1/oauth/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "public-client" {
			t.Errorf("bad refresh form: %v", r.Form)
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"rotated-refresh","token_type":"bearer","expires_in":3600}`)
	}))
	defer server.Close()
	store := memoryStore{}
	c := &client{origin: "https://site.example", http: secureHTTP(), store: store, config: &discovery{SupabaseURL: server.URL, OAuthClientID: "public-client", PublishableKey: "public-key"}}
	if err := c.saveSession(session{"old-access", "old-refresh", time.Now().Unix() - 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		token, err := c.accessToken(context.Background())
		if err != nil || token != "new-access" {
			t.Fatalf("%q %v", token, err)
		}
	}
	if calls != 1 || !strings.Contains(store[c.origin], "rotated-refresh") {
		t.Fatal("refresh not rotated and cached")
	}
	if _, err := store.Get("https://other.example"); err == nil {
		t.Fatal("session origin isolation failed")
	}
}

func TestExchangeAuthorizationCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		for k, v := range map[string]string{"grant_type": "authorization_code", "client_id": "client", "code": "one-time-code", "redirect_uri": "http://127.0.0.1:43821/callback", "code_verifier": "verifier"} {
			if r.Form.Get(k) != v {
				t.Errorf("%s missing from code exchange", k)
			}
		}
		fmt.Fprint(w, `{"access_token":"a","refresh_token":"r","token_type":"bearer","expires_in":3600}`)
	}))
	defer server.Close()
	c := &client{http: secureHTTP()}
	_, err := c.exchange(context.Background(), &discovery{SupabaseURL: server.URL}, url.Values{"grant_type": {"authorization_code"}, "client_id": {"client"}, "code": {"one-time-code"}, "redirect_uri": {"http://127.0.0.1:43821/callback"}, "code_verifier": {"verifier"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoginCancellation(t *testing.T) {
	c := &client{origin: "https://test.example", http: secureHTTP(), store: memoryStore{}, config: &discovery{SupabaseURL: "https://auth.example", OAuthClientID: "client", RedirectURIs: []string{"http://127.0.0.1:43821/callback", "http://127.0.0.1:43822/callback", "http://127.0.0.1:43823/callback"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.login(ctx, io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("%v", err)
	}
}

const projectTestID = "11111111-1111-1111-1111-111111111111"
const fileTestID = "22222222-2222-2222-2222-222222222222"

func TestPushLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "transfer-failure", "completion-failure"} {
		t.Run(mode, func(t *testing.T) {
			deleted, completed, uploaded := false, false, false
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/storage/") {
					if r.Header.Get("Authorization") != "" || r.Header.Get("apikey") != "public-key" || r.Header.Get("Content-Type") != "text/plain" || r.URL.Query().Get("token") != "signed-token" || r.Method != "PUT" {
						t.Error("invalid storage request or leaked app bearer")
					}
					b, _ := io.ReadAll(r.Body)
					if string(b) != "hello $x$" {
						t.Error("wrong original bytes")
					}
					uploaded = true
					if mode == "transfer-failure" {
						w.WriteHeader(500)
					}
					return
				}
				if r.Header.Get("Authorization") != "Bearer user-access" {
					t.Error("metadata auth missing")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/uploads"):
					if mode == "conflict" {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"error":{"code":"conflict","message":"File already exists"}}`)
						return
					}
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["name"] != "paper.tex" || body["sizeBytes"] != float64(9) || body["folderId"] != nil {
						t.Errorf("wrong reservation: %v", body)
					}
					_ = json.NewEncoder(w).Encode(preparedUpload{fileTestID, "project-files", "owner/project/file", "signed-token", "text/plain"})
				case strings.HasSuffix(r.URL.Path, "/complete"):
					completed = true
					if mode == "completion-failure" {
						w.WriteHeader(500)
						return
					}
					fmt.Fprintf(w, `{"file":{"id":%q,"name":"paper.tex"}}`, fileTestID)
				case r.Method == "DELETE":
					deleted = true
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "paper.tex")
			if err := os.WriteFile(path, []byte("hello $x$"), 0600); err != nil {
				t.Fatal(err)
			}
			c := &client{origin: server.URL, http: secureHTTP(), tokenOverride: "user-access", config: &discovery{SupabaseURL: server.URL, PublishableKey: "public-key"}}
			f, err := c.push(context.Background(), projectTestID, nil, path)
			switch mode {
			case "success":
				if err != nil || f.ID != fileTestID || !completed || deleted {
					t.Fatalf("%+v %v", f, err)
				}
			case "conflict":
				var apiErr *apiError
				if !errors.As(err, &apiErr) || apiErr.Status != 409 || uploaded || deleted {
					t.Fatal("conflict did not stop upload")
				}
			case "transfer-failure":
				if err == nil || !deleted || completed {
					t.Fatal("failed transfer did not clean up")
				}
			case "completion-failure":
				if err == nil || deleted || !completed {
					t.Fatal("ambiguous completion incorrectly deleted file")
				}
			}
		})
	}
}

func TestUploadValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{{"ok.tex", "\\begin{document}α\n", true}, {"ok.pdf", "%PDF-1.7\nexample\n%%EOF", true}, {"bad.pdf", "not a pdf", false}, {"bad.tex", "\xff", false}, {"binary.md", "text\x00", false}, {"bad.exe", "x", false}, {"empty.tex", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			_ = os.WriteFile(path, []byte(tc.body), 0600)
			_, err := readUpload(path)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected validation: %v", err)
			}
		})
	}
}

func TestDownloadDoesNotSendTokenOrOverwrite(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signed" {
			if r.Header.Get("Authorization") != "" {
				t.Error("token leaked to signed download")
			}
			fmt.Fprint(w, "original")
			return
		}
		fmt.Fprintf(w, `{"url":%q}`, server.URL+"/signed?token=signed")
	}))
	defer server.Close()
	c := &client{origin: server.URL, http: secureHTTP(), tokenOverride: "user-token"}
	path := filepath.Join(t.TempDir(), "paper.tex")
	if err := c.download(context.Background(), projectTestID, fileTestID, path); err != nil {
		t.Fatal(err)
	}
	if err := c.download(context.Background(), projectTestID, fileTestID, path); err == nil {
		t.Fatal("overwrote existing destination")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "original" {
		t.Fatal("original changed")
	}
}

func TestProjectCommandAndSelection(t *testing.T) {
	t.Setenv("NNUN_TOKEN", "explicit-user-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer explicit-user-token" {
			t.Error("explicit token was not used")
		}
		if r.URL.Path == "/api/projects" {
			fmt.Fprintf(w, `{"projects":[{"id":%q,"name":"Research","visibility":"private"}]}`, projectTestID)
			return
		}
		fmt.Fprintf(w, `{"project":{"id":%q,"name":"Research","visibility":"private"},"folders":[],"files":[],"canEdit":true}`, projectTestID)
	}))
	defer server.Close()
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"projects", "list", "--json"}, {"use", "Research"}, {"files", "list", "--json"}} {
		var out bytes.Buffer
		command := newCommand()
		command.SetOut(&out)
		command.SetErr(&out)
		command.SetArgs(append(args, "--api-url", server.URL))
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("empty command output")
		}
	}
	cfg, err := readLocalConfig()
	if err != nil || cfg.ProjectID != projectTestID || cfg.APIURL != server.URL {
		t.Fatalf("invalid selection: %+v %v", cfg, err)
	}
	if _, err := selectedProject("", "https://other.example"); err == nil {
		t.Fatal("selection crossed API origins")
	}
	if _, err := selectedProject("../escape", server.URL); err == nil {
		t.Fatal("accepted invalid project identifier")
	}
}
