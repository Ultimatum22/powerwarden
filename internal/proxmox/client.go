package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient is the real Client implementation, talking to the Proxmox VE
// API over HTTPS with the TLS certificate pinned by fingerprint (Proxmox
// nodes normally present a self-signed cert; verifying against the system
// root store would either fail or, worse, tempt someone into disabling
// verification entirely).
type HTTPClient struct {
	baseURL     string // e.g. https://10.22.10.5:8006
	node        string
	tokenID     string
	tokenSecret string

	httpClient *http.Client
}

// NewHTTPClient builds a Client pinned to the given SHA-256 TLS fingerprint
// (colon-separated hex, as printed by Proxmox's own UI, e.g. "AB:CD:...").
func NewHTTPClient(baseURL, node, tokenID, tokenSecret, tlsFingerprint string) (*HTTPClient, error) {
	want, err := parseFingerprint(tlsFingerprint)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			// Verification is done manually against the pinned
			// fingerprint in VerifyPeerCertificate, since Proxmox
			// nodes present a self-signed cert that a normal chain
			// verification would reject.
			InsecureSkipVerify: true, //nolint:gosec // pinned below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("proxmox: server presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				if sum != want {
					return fmt.Errorf("proxmox: TLS fingerprint mismatch: got %s, want %s",
						hex.EncodeToString(sum[:]), hex.EncodeToString(want[:]))
				}
				return nil
			},
		},
	}

	return &HTTPClient{
		baseURL:     strings.TrimRight(baseURL, "/"),
		node:        node,
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		},
	}, nil
}

func parseFingerprint(fp string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	clean := strings.ReplaceAll(fp, ":", "")
	b, err := hex.DecodeString(clean)
	if err != nil {
		return out, fmt.Errorf("proxmox: invalid tls_fingerprint %q: %w", fp, err)
	}
	if len(b) != sha256.Size {
		return out, fmt.Errorf("proxmox: tls_fingerprint %q must be a SHA-256 fingerprint (%d bytes, got %d)", fp, sha256.Size, len(b))
	}
	copy(out[:], b)
	return out, nil
}

type apiEnvelope[T any] struct {
	Data T `json:"data"`
}

func (c *HTTPClient) do(ctx context.Context, method, path string, query url.Values, body url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = strings.NewReader(body.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("proxmox: build request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.tokenSecret))
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("proxmox: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox: %s %s: status %d: %s", method, path, resp.StatusCode, truncate(string(data), 500))
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("proxmox: decode response from %s %s: %w", method, path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type clusterResource struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // "qemu" or "lxc" (also "node", "storage", ... filtered out by type=vm)
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`
}

func (c *HTTPClient) ListGuests(ctx context.Context) ([]Guest, error) {
	var env apiEnvelope[[]clusterResource]
	query := url.Values{"type": {"vm"}}
	if err := c.do(ctx, http.MethodGet, "/api2/json/cluster/resources", query, nil, &env); err != nil {
		return nil, err
	}
	guests := make([]Guest, 0, len(env.Data))
	for _, r := range env.Data {
		var kind GuestKind
		switch r.Type {
		case string(KindQEMU):
			kind = KindQEMU
		case string(KindLXC):
			kind = KindLXC
		default:
			continue
		}
		status := StatusStopped
		if r.Status == string(StatusRunning) {
			status = StatusRunning
		}
		guests = append(guests, Guest{
			VMID:   r.VMID,
			Name:   r.Name,
			Kind:   kind,
			Node:   r.Node,
			Status: status,
		})
	}
	return guests, nil
}

type nodeStatusResponse struct {
	Uptime int64 `json:"uptime"`
}

func (c *HTTPClient) NodeStatus(ctx context.Context) (NodeStatus, error) {
	var env apiEnvelope[nodeStatusResponse]
	path := fmt.Sprintf("/api2/json/nodes/%s/status", c.node)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &env); err != nil {
		return NodeStatus{}, err
	}
	return NodeStatus{Node: c.node, Uptime: env.Data.Uptime}, nil
}

func (c *HTTPClient) StartGuest(ctx context.Context, kind GuestKind, vmid int) (UPID, error) {
	return c.guestAction(ctx, kind, vmid, "start")
}

func (c *HTTPClient) ShutdownGuest(ctx context.Context, kind GuestKind, vmid int) (UPID, error) {
	return c.guestAction(ctx, kind, vmid, "shutdown")
}

func (c *HTTPClient) guestAction(ctx context.Context, kind GuestKind, vmid int, action string) (UPID, error) {
	var env apiEnvelope[string]
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/%s", c.node, kind, vmid, action)
	if err := c.do(ctx, http.MethodPost, path, nil, url.Values{}, &env); err != nil {
		return "", err
	}
	return UPID(env.Data), nil
}

type taskStatusResponse struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus"`
}

func (c *HTTPClient) TaskStatus(ctx context.Context, upid UPID) (TaskStatus, error) {
	var env apiEnvelope[taskStatusResponse]
	path := fmt.Sprintf("/api2/json/nodes/%s/tasks/%s/status", c.node, url.PathEscape(string(upid)))
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &env); err != nil {
		return TaskStatus{}, err
	}
	state := TaskState(env.Data.Status)
	if env.Data.Status == "stopped" {
		if env.Data.ExitStatus == "OK" {
			state = TaskOK
		} else {
			state = TaskError
		}
	}
	return TaskStatus{State: state, ExitStatus: env.Data.ExitStatus}, nil
}

type activeTask struct {
	UPID string `json:"upid"`
	Type string `json:"type"`
	User string `json:"user"`
}

func (c *HTTPClient) ActiveTasks(ctx context.Context) ([]Task, error) {
	var env apiEnvelope[[]activeTask]
	path := fmt.Sprintf("/api2/json/nodes/%s/tasks", c.node)
	query := url.Values{"source": {"active"}}
	if err := c.do(ctx, http.MethodGet, path, query, nil, &env); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(env.Data))
	for _, t := range env.Data {
		tasks = append(tasks, Task{UPID: UPID(t.UPID), Type: t.Type, User: t.User})
	}
	return tasks, nil
}

func (c *HTTPClient) ShutdownHost(ctx context.Context) (UPID, error) {
	var env apiEnvelope[string]
	path := fmt.Sprintf("/api2/json/nodes/%s/status", c.node)
	body := url.Values{"command": {"shutdown"}}
	if err := c.do(ctx, http.MethodPost, path, nil, body, &env); err != nil {
		return "", err
	}
	return UPID(env.Data), nil
}

var _ Client = (*HTTPClient)(nil)
