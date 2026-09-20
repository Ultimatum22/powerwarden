package wol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"text/template"
)

// RouterAPISender wakes the host through the router/firewall's own WoL
// service. The exact API varies by vendor and is TODO(owner) until the
// router model is known (see CLAUDE.md), so the request is built from a
// user-supplied template rather than a hardcoded vendor client.
type RouterAPISender struct {
	// URL and Body may reference {{.MAC}} (colon-separated lowercase, e.g.
	// "aa:bb:cc:dd:ee:ff").
	URL     string
	Method  string // defaults to POST
	Headers map[string]string
	Body    string

	// HTTPClient is overridable in tests; defaults to http.DefaultClient.
	HTTPClient *http.Client
}

type templateData struct {
	MAC string
}

func (s RouterAPISender) Send(ctx context.Context, mac net.HardwareAddr) error {
	data := templateData{MAC: mac.String()}

	url, err := renderTemplate("wol.router_api.url", s.URL, data)
	if err != nil {
		return err
	}
	body, err := renderTemplate("wol.router_api.body", s.Body, data)
	if err != nil {
		return err
	}

	method := s.Method
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("wol: build router_api request: %w", err)
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}

	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("wol: router_api request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wol: router_api request to %s returned status %d", url, resp.StatusCode)
	}
	return nil
}

func renderTemplate(name, text string, data templateData) (string, error) {
	tmpl, err := template.New(name).Parse(text)
	if err != nil {
		return "", fmt.Errorf("wol: parse %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("wol: render %s template: %w", name, err)
	}
	return buf.String(), nil
}
