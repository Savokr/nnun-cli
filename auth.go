package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

type credentialStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}
type systemStore struct{}

func (systemStore) Get(origin string) (string, error) { return keyring.Get("nnun-cli", origin) }
func (systemStore) Set(origin, value string) error    { return keyring.Set("nnun-cli", origin, value) }
func (systemStore) Delete(origin string) error {
	err := keyring.Delete("nnun-cli", origin)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (c *client) saveSession(s session) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := c.store.Set(c.origin, string(b)); err != nil {
		return errors.New("cannot save credentials in the OS credential store; unlock or configure your keyring, or use an explicitly supplied NNUN_TOKEN")
	}
	return nil
}

func (c *client) accessToken(ctx context.Context) (string, error) {
	if c.tokenOverride != "" {
		return c.tokenOverride, nil
	}
	b, err := c.store.Get(c.origin)
	if err != nil {
		return "", errors.New("no readable login in the OS credential store; run nnun login or supply NNUN_TOKEN")
	}
	var s session
	if err := json.Unmarshal([]byte(b), &s); err != nil || s.RefreshToken == "" {
		return "", errors.New("invalid stored session; run nnun login")
	}
	if s.AccessToken != "" && time.Now().Unix()+60 < s.ExpiresAt {
		return s.AccessToken, nil
	}
	d, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	s, err = c.exchange(ctx, d, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}, "client_id": {d.OAuthClientID}})
	if err != nil {
		return "", fmt.Errorf("session refresh failed; run nnun login: %w", err)
	}
	if err := c.saveSession(s); err != nil {
		return "", err
	}
	return s.AccessToken, nil
}

func (c *client) exchange(ctx context.Context, d *discovery, values url.Values) (session, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", d.SupabaseURL+"/auth/v1/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("apikey", d.PublishableKey)
	res, err := c.http.Do(req)
	if err != nil {
		return session{}, errors.New("token exchange could not reach the authentication server")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return session{}, fmt.Errorf("authentication server rejected token exchange (HTTP %d)", res.StatusCode)
	}
	var t tokenResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&t); err != nil {
		return session{}, errors.New("invalid token response")
	}
	if t.AccessToken == "" || t.RefreshToken == "" || t.ExpiresIn <= 0 || !strings.EqualFold(t.TokenType, "bearer") {
		return session{}, errors.New("authentication server returned incomplete credentials")
	}
	return session{t.AccessToken, t.RefreshToken, time.Now().Unix() + t.ExpiresIn}, nil
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func pkceChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// Reject invalid callbacks without ending the real login attempt.
func callbackHandler(state string, result chan<- string, denied chan<- error) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		if r.Method != "GET" || r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid login state. Return to your original login tab.", 400)
			return
		}
		if r.URL.Query().Get("error") != "" {
			once.Do(func() { denied <- errors.New("login was declined or expired") })
			http.Error(w, "Login was declined. You can close this tab.", 400)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code.", 400)
			return
		}
		once.Do(func() { result <- code })
		_, _ = io.WriteString(w, "Nnun received your authorization. Return to your terminal to finish login. You can close this tab.")
	})
}

func bindCallback(uris []string) (net.Listener, string, error) {
	allowed := map[string]bool{"http://127.0.0.1:43821/callback": true, "http://127.0.0.1:43822/callback": true, "http://127.0.0.1:43823/callback": true}
	for _, raw := range uris {
		if !allowed[raw] {
			continue
		}
		u, _ := url.Parse(raw)
		listener, err := net.Listen("tcp4", u.Host)
		if err == nil {
			return listener, raw, nil
		}
	}
	return nil, "", errors.New("no registered login port is available (127.0.0.1:43821–43823)")
}

func openBrowser(ctx context.Context, target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", target)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	return cmd.Run()
}

func (c *client) login(ctx context.Context, out io.Writer, noBrowser bool) error {
	if c.tokenOverride != "" {
		return errors.New("unset NNUN_TOKEN before interactive login")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	d, err := c.discover(ctx)
	if err != nil {
		return err
	}
	// Check storage before authorizing so a machine without a keyring fails clearly.
	probe, err := randomSecret()
	if err != nil {
		return err
	}
	if err := c.store.Set(c.origin+"/login-check", probe); err != nil {
		return errors.New("OS credential store is unavailable; unlock/configure it or use NNUN_TOKEN")
	}
	_ = c.store.Delete(c.origin + "/login-check")
	listener, redirect, err := bindCallback(d.RedirectURIs)
	if err != nil {
		return err
	}
	defer listener.Close()
	state, err := randomSecret()
	if err != nil {
		return err
	}
	verifier, err := randomSecret()
	if err != nil {
		return err
	}
	codes := make(chan string, 1)
	denied := make(chan error, 1)
	server := &http.Server{Handler: callbackHandler(state, codes, denied), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	values := url.Values{"response_type": {"code"}, "client_id": {d.OAuthClientID}, "redirect_uri": {redirect}, "scope": {"email"}, "prompt": {"consent"}, "state": {state}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}}
	target := d.SupabaseURL + "/auth/v1/oauth/authorize?" + values.Encode()
	fmt.Fprintln(out, "Approve Nnun CLI access in your browser:", target)
	if !noBrowser {
		if err := openBrowser(ctx, target); err != nil {
			fmt.Fprintln(out, "Could not open a browser automatically. Open the URL above on this computer.")
		}
	}
	var code string
	select {
	case code = <-codes:
	case err := <-denied:
		return err
	case <-ctx.Done():
		return errors.New("login cancelled or timed out; run nnun login to try again")
	}
	s, err := c.exchange(ctx, d, url.Values{"grant_type": {"authorization_code"}, "client_id": {d.OAuthClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		return err
	}
	if err := c.saveSession(s); err != nil {
		return err
	}
	fmt.Fprintln(out, "Logged in. Run nnun projects list to get started.")
	return nil
}

func (c *client) whoami(ctx context.Context, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	d, err := c.discover(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.SupabaseURL+"/auth/v1/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("apikey", d.PublishableKey)
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("could not verify account")
	}
	defer res.Body.Close()
	return decodeResponse(res, out)
}
