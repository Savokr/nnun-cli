package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPI = "https://v0-knowledge-graph-explorer-ten.vercel.app"

type discovery struct {
	APIVersion     int      `json:"apiVersion"`
	SupabaseURL    string   `json:"supabaseUrl"`
	PublishableKey string   `json:"publishableKey"`
	OAuthClientID  string   `json:"oauthClientId"`
	RedirectURIs   []string `json:"redirectUris"`
}

type project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}
type folder struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
}
type projectFile struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	FolderID  *string `json:"folderId"`
	SizeBytes int64   `json:"sizeBytes"`
}
type projectTree struct {
	Project project       `json:"project"`
	Folders []folder      `json:"folders"`
	Files   []projectFile `json:"files"`
	CanEdit bool          `json:"canEdit"`
}
type preparedUpload struct {
	FileID      string `json:"fileId"`
	Bucket      string `json:"bucket"`
	Path        string `json:"path"`
	Token       string `json:"token"`
	ContentType string `json:"contentType"`
}
type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

// Redirects stay on the original origin, including requests with signed URLs.
func secureHTTP() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			return errors.New("cross-origin redirect refused")
		}
		return nil
	}}
}

func validateOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("API URL must be an origin without a path, query or credentials")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return "", errors.New("HTTPS is required except for loopback development servers")
	}
	return strings.TrimRight(raw, "/"), nil
}

type client struct {
	origin        string
	http          *http.Client
	store         credentialStore
	config        *discovery
	tokenOverride string
}

func (c *client) discover(ctx context.Context) (*discovery, error) {
	if c.config != nil {
		return c.config, nil
	}
	var d discovery
	if err := c.request(ctx, "GET", c.origin+"/api/cli/config", "", nil, &d); err != nil {
		return nil, err
	}
	if d.APIVersion != 1 || d.PublishableKey == "" || d.OAuthClientID == "" {
		return nil, errors.New("server CLI configuration is incomplete or incompatible")
	}
	origin, err := validateOrigin(d.SupabaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid authentication server: %w", err)
	}
	d.SupabaseURL = origin
	c.config = &d
	return &d, nil
}

func (c *client) request(ctx context.Context, method, target, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("request failed; check connection and server URL")
	}
	defer res.Body.Close()
	return decodeResponse(res, out)
}

func decodeResponse(res *http.Response, out any) error {
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var b struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&b)
		if b.Error.Message == "" {
			b.Error.Message = http.StatusText(res.StatusCode)
		}
		if b.Error.Code == "" {
			b.Error.Code = "request_failed"
		}
		return &apiError{res.StatusCode, b.Error.Code, b.Error.Message}
	}
	if out == nil || res.StatusCode == 204 {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

func (c *client) api(ctx context.Context, method, path string, body, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	return c.request(ctx, method, c.origin+"/api/projects"+path, token, body, out)
}
