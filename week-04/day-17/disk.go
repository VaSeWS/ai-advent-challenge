package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const diskAPIURL = "https://cloud-api.yandex.net/v1/disk/resources"

var videoPath = regexp.MustCompile(`^week-0*([1-9][0-9]*)/day-([0-9][0-9])/video/[^/]+\.mp4$`)

type uploadInput struct {
	File string `json:"file" jsonschema:"Path to a local week-NN/day-NN/video/*.mp4 recording in this repository"`
}

type uploadOutput struct {
	Path      string `json:"path" jsonschema:"Destination path on Yandex Disk"`
	PublicURL string `json:"public_url" jsonschema:"Published public link to the uploaded recording"`
}

type diskAPI struct {
	baseURL string
	token   string
	client  *http.Client
	root    string
}

func (d *diskAPI) request(ctx context.Context, method, endpoint string, query url.Values, body io.Reader, target any) error {
	uri := d.baseURL + endpoint
	if query != nil {
		uri += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, uri, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+d.token)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Yandex Disk %s: HTTP %d", endpoint, resp.StatusCode)
	}
	if target != nil {
		return json.NewDecoder(resp.Body).Decode(target)
	}
	return nil
}

func (d *diskAPI) upload(ctx context.Context, in uploadInput) (uploadOutput, error) {
	if d.token == "" {
		return uploadOutput{}, errors.New("YANDEX_DISK_TOKEN is not set")
	}
	abs, err := filepath.Abs(in.File)
	if err != nil {
		return uploadOutput{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return uploadOutput{}, err
	}
	root, err := filepath.EvalSymlinks(d.root)
	if err != nil {
		return uploadOutput{}, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil {
		return uploadOutput{}, err
	}
	match := videoPath.FindStringSubmatch(filepath.ToSlash(rel))
	if match == nil {
		return uploadOutput{}, errors.New("recording must be a week-NN/day-NN/video/*.mp4 file in the repository")
	}
	file, err := os.Open(real)
	if err != nil {
		return uploadOutput{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return uploadOutput{}, errors.New("recording must be a regular file")
	}
	remoteDir := "/ai-advent-challenge/week-" + match[1]
	remotePath := remoteDir + "/" + filepath.Base(real)
	for _, dir := range []string{"/ai-advent-challenge", remoteDir} {
		err := d.request(ctx, http.MethodPut, "", url.Values{"path": {dir}}, nil, nil)
		if err != nil && !strings.Contains(err.Error(), "HTTP 409") {
			return uploadOutput{}, fmt.Errorf("create folder %s: %w", dir, err)
		}
	}
	var link struct {
		Href string `json:"href"`
	}
	if err := d.request(ctx, http.MethodGet, "/upload", url.Values{"path": {remotePath}, "overwrite": {"true"}}, nil, &link); err != nil {
		return uploadOutput{}, fmt.Errorf("get upload URL: %w", err)
	}
	if link.Href == "" {
		return uploadOutput{}, errors.New("Yandex Disk returned no upload URL")
	}
	uploadURL, err := url.Parse(link.Href)
	if err != nil || uploadURL.Scheme != "https" || uploadURL.Host == "" {
		return uploadOutput{}, errors.New("Yandex Disk returned an invalid upload URL")
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, link.Href, file)
	if err != nil {
		return uploadOutput{}, err
	}
	put.ContentLength = info.Size()
	resp, err := d.client.Do(put)
	if err != nil {
		return uploadOutput{}, fmt.Errorf("upload recording: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return uploadOutput{}, fmt.Errorf("upload recording: HTTP %d", resp.StatusCode)
	}
	if err := d.request(ctx, http.MethodPut, "/publish", url.Values{"path": {remotePath}}, nil, nil); err != nil {
		return uploadOutput{}, fmt.Errorf("publish recording: %w", err)
	}
	var metadata struct {
		PublicURL string `json:"public_url"`
	}
	if err := d.request(ctx, http.MethodGet, "", url.Values{"path": {remotePath}, "fields": {"public_url"}}, nil, &metadata); err != nil {
		return uploadOutput{}, fmt.Errorf("read public link: %w", err)
	}
	if metadata.PublicURL == "" {
		return uploadOutput{}, errors.New("Yandex Disk returned no public link")
	}
	return uploadOutput{Path: remotePath, PublicURL: metadata.PublicURL}, nil
}

func newDiskAPI(root string) *diskAPI {
	client := &http.Client{Timeout: 10 * time.Minute}
	if os.Getenv("YANDEX_DISK_IPV4") == "1" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", address)
		}
		client.Transport = transport
	}
	return &diskAPI{baseURL: diskAPIURL, token: os.Getenv("YANDEX_DISK_TOKEN"), client: client, root: root}
}

func registerDiskTool(server *mcp.Server, disk *diskAPI) {
	mcp.AddTool(server, &mcp.Tool{Name: "publish_video", Description: "Upload a local homework demo MP4 to Yandex Disk and return its public URL."}, func(ctx context.Context, _ *mcp.CallToolRequest, input uploadInput) (*mcp.CallToolResult, uploadOutput, error) {
		output, err := disk.upload(ctx, input)
		return nil, output, err
	})
}
