package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func readUpload(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("upload only regular files")
	}
	ext := strings.ToLower(filepath.Ext(path))
	limit := int64(2 << 20)
	switch ext {
	case ".tex", ".txt", ".md":
	case ".pdf":
		limit = 6 << 20
	default:
		return nil, errors.New("choose a .tex, .txt, .md, or .pdf file")
	}
	if info.Size() <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("%s must contain 1–%d bytes", filepath.Base(path), limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file grew beyond the upload limit")
	}
	if ext == ".pdf" {
		if !(bytes.HasPrefix(b, []byte("%PDF-1.")) || bytes.HasPrefix(b, []byte("%PDF-2.0"))) {
			return nil, errors.New("invalid PDF header")
		}
		start := len(b) - 1024
		if start < 0 {
			start = 0
		}
		if !bytes.Contains(b[start:], []byte("%%EOF")) {
			return nil, errors.New("PDF is incomplete")
		}
	} else {
		if !utf8.Valid(b) {
			return nil, errors.New("text must be valid UTF-8")
		}
		for _, r := range b {
			if r < 9 || (r > 10 && r < 13) || (r > 13 && r < 32) || r == 127 {
				return nil, errors.New("text contains binary control characters")
			}
		}
	}
	return b, nil
}

func (c *client) push(ctx context.Context, projectID string, folderID *string, path string) (projectFile, error) {
	b, err := readUpload(path)
	if err != nil {
		return projectFile{}, err
	}
	d, err := c.discover(ctx)
	if err != nil {
		return projectFile{}, err
	}
	var prepared preparedUpload
	if err := c.api(ctx, "POST", "/"+projectID+"/uploads", map[string]any{"name": filepath.Base(path), "folderId": folderID, "sizeBytes": len(b)}, &prepared); err != nil {
		return projectFile{}, err
	}
	if identifier(prepared.FileID) != nil || prepared.Bucket != "project-files" || prepared.Path == "" || prepared.Token == "" || (prepared.ContentType != "text/plain" && prepared.ContentType != "application/pdf") {
		return projectFile{}, errors.New("server returned invalid upload reservation")
	}
	u, _ := url.Parse(d.SupabaseURL)
	u.Path = "/storage/v1/object/upload/sign/" + prepared.Bucket + "/" + prepared.Path
	u.RawQuery = url.Values{"token": {prepared.Token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "PUT", u.String(), bytes.NewReader(b))
	if err != nil {
		return projectFile{}, err
	}
	req.Header.Set("Content-Type", prepared.ContentType)
	req.Header.Set("apikey", d.PublishableKey)
	req.Header.Set("x-upsert", "false")
	res, transferErr := c.http.Do(req)
	if transferErr == nil {
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			transferErr = fmt.Errorf("storage rejected upload (HTTP %d)", res.StatusCode)
		}
		res.Body.Close()
	}
	if transferErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleanupErr := c.api(cleanupCtx, "DELETE", "/"+projectID+"/files/"+prepared.FileID, nil, nil)
		if cleanupErr != nil {
			return projectFile{}, fmt.Errorf("upload failed; reservation %s could not be cancelled; list projects later to allow stale-upload cleanup", prepared.FileID)
		}
		return projectFile{}, errors.New("upload transfer failed; reservation cancelled, safe to try again")
	}
	var completed struct {
		File projectFile `json:"file"`
	}
	if err := c.api(ctx, "POST", "/"+projectID+"/files/"+prepared.FileID+"/complete", nil, &completed); err != nil {
		// Completion may have succeeded even if its response was lost. Never delete
		// this reservation: it may now be a completed, immutable original.
		return projectFile{}, fmt.Errorf("bytes uploaded but confirmation failed for file %s; inspect nnun files list before retrying (completion is idempotent): %w", prepared.FileID, err)
	}
	return completed.File, nil
}

func (c *client) download(ctx context.Context, projectID, fileID, path string) error {
	var signed struct {
		URL string `json:"url"`
	}
	if err := c.api(ctx, "GET", "/"+projectID+"/files/"+fileID+"/download", nil, &signed); err != nil {
		return err
	}
	u, err := url.Parse(signed.URL)
	if err != nil || u.User != nil || u.Fragment != "" {
		return errors.New("invalid download URL")
	}
	if _, err := validateOrigin(u.Scheme + "://" + u.Host); err != nil {
		return err
	}
	// Signed downloads intentionally contain no application bearer token.
	req, err := http.NewRequestWithContext(ctx, "GET", signed.URL, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("download failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download failed (HTTP %d)", res.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(path)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(res.Body, (6<<20)+1))
	if err != nil {
		return err
	}
	if n > 6<<20 {
		return errors.New("download exceeds the supported file limit")
	}
	if err := f.Close(); err != nil {
		return err
	}
	success = true
	return nil
}
