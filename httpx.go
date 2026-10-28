package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpClient replaces axios + proxy-agent.js: http(s), socks4 and socks5 proxies, optional insecure TLS.
func httpClient(proxy string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // like axios with proxy: false; only the explicit proxy is used
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("proxy: %w", err)
		}
		switch {
		case strings.HasPrefix(u.Scheme, "socks4"): // net/http has no socks4
			tr.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
				return dialVia(proxy, addr, 30*time.Second)
			}
		case strings.HasPrefix(u.Scheme, "http"), strings.HasPrefix(u.Scheme, "socks5"):
			tr.Proxy = http.ProxyURL(u)
		default:
			return nil, fmt.Errorf("proxy type %q not supported", u.Scheme)
		}
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// httpRequest is what the UI shim asks /api/http to perform (AI providers, sync servers).
type httpRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Proxy   string            `json:"proxy"`
}

// handleHTTP performs the request and streams the upstream body back; the upstream status
// travels in X-Upstream-Status so streaming (SSE from AI providers) keeps working.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	var req httpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		http.Error(w, "only http(s) URLs", http.StatusBadRequest)
		return
	}
	c, err := httpClient(req.Proxy, false, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	up, err := http.NewRequestWithContext(r.Context(), method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for k, v := range req.Headers {
		up.Header.Set(k, v)
	}
	resp, err := c.Do(up)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("X-Upstream-Status", fmt.Sprint(resp.StatusCode))
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// axiosLike runs an axios-style options object ({url, method, headers, data, params, timeout})
// and returns the parsed response body; errors carry the status like axios' messages.
func axiosLike(o map[string]any, proxy string, insecure bool) (any, error) {
	rawURL := str(o, "url")
	if base := str(o, "baseURL"); base != "" && !strings.HasPrefix(rawURL, "http") {
		rawURL = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(rawURL, "/")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid url %q", rawURL)
	}
	if params := asMap(o["params"]); params != nil {
		q := u.Query()
		for k, v := range params {
			q.Set(k, fmt.Sprint(v))
		}
		u.RawQuery = q.Encode()
	}
	var body io.Reader
	switch d := o["data"].(type) {
	case nil:
	case string:
		body = strings.NewReader(d)
	default:
		b, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	timeout := 60 * time.Second
	if t, ok := o["timeout"].(float64); ok && t > 0 {
		timeout = time.Duration(t) * time.Millisecond
	}
	c, err := httpClient(proxy, insecure, timeout)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(str(o, "method"))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range asMap(o["headers"]) {
		req.Header.Set(k, fmt.Sprint(v))
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data any = string(raw)
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		data = parsed
	}
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{status: resp.StatusCode, statusText: http.StatusText(resp.StatusCode), data: data}
	}
	return data, nil
}

// httpStatusError formats like electerm-sync's HTTPError.
type httpStatusError struct {
	status     int
	statusText string
	data       any
}

func (e *httpStatusError) Error() string {
	msg := fmt.Sprintf("status: %d\nstatusText: %s", e.status, e.statusText)
	d := ""
	switch v := e.data.(type) {
	case string:
		d = v
	case map[string]any:
		if m, ok := asMap(v["error"])["message"].(string); ok {
			d = m
		} else if m, ok := v["message"].(string); ok {
			d = m
		} else {
			b, _ := json.Marshal(v)
			d = string(b)
		}
	}
	if d = strings.TrimSpace(d); d != "" {
		if len(d) > 300 {
			d = d[:300] + fmt.Sprintf("... (%d chars)", len(d))
		}
		msg += "\ndata: " + d
	}
	return msg
}
