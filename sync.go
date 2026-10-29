package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// doSync is server/sync.js: electerm-sync for github / gitee / custom / cloud, webdav-sync.js for webdav.
func doSync(typ, fn string, args []any, token, proxy string) (any, error) {
	if typ == "webdav" {
		return webdavSync(fn, args, token, proxy)
	}
	if typ == "cloud" && len(args) > 0 {
		args[0] = ""
	}
	var server, auth string
	switch typ {
	case "github":
		server, auth = "https://api.github.com", "token "+token
	case "gitee":
		server, auth = "https://gitee.com/api/v5", "token "+token
	default: // custom, cloud: "token####server####userId####algorithm"
		parts := append(strings.Split(token, "####"), "", "", "", "")
		secret, userID, alg := parts[0], parts[2], parts[3]
		server = parts[1]
		if alg == "" {
			alg = "HS256"
		}
		if fn != "create" && len(args) > 0 && (fn != "update" || len(args) > 1) {
			if id, ok := args[0].(string); ok {
				userID = id
			}
		}
		auth = "Bearer " + secret
		if userID != "" {
			jwt, err := signJWT(map[string]any{"id": userID, "exp": time.Now().UnixMilli() + 1e6, "iat": time.Now().Unix()}, secret, alg)
			if err != nil {
				return nil, err
			}
			auth = "Bearer " + jwt
		}
	}
	if server == "" {
		return nil, fmt.Errorf("sync server URL is not configured")
	}
	headers := map[string]any{"Authorization": auth, "X-User-Agent": "electerm-sync/v2.0.0"}
	req := map[string]any{"headers": headers}
	gist := typ == "github" || typ == "gitee"
	switch {
	case fn == "test" && gist:
		req["url"], req["method"] = server+"/gists?per_page=1", "GET"
	case fn == "test":
		req["url"], req["method"] = server, "POST"
	case fn == "create" && gist:
		req["url"], req["method"], req["data"] = server+"/gists", "POST", arg0(args)
	case fn == "create":
		req["url"], req["method"], req["data"] = server, "PUT", arg0(args)
	case fn == "update" && gist:
		req["url"], req["method"], req["data"] = server+"/gists/"+argStr(args, 0), "PATCH", argAt(args, 1)
	case fn == "update":
		req["url"], req["method"] = server, "PUT"
		if len(args) > 1 {
			req["data"] = args[1]
		} else {
			req["data"] = arg0(args)
		}
	case fn == "getOne" && gist:
		req["url"], req["method"] = server+"/gists/"+argStr(args, 0), "GET"
	case fn == "getOne":
		req["url"], req["method"] = server, "GET"
	default:
		return nil, fmt.Errorf("unsupported func: %s", fn)
	}
	return axiosLike(req, proxy, false)
}

func argAt(args []any, i int) any {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// signJWT is the jsonwebtoken.sign subset electerm-sync uses (HMAC algorithms).
func signJWT(claims map[string]any, secret, alg string) (string, error) {
	var h func() hash.Hash
	switch alg {
	case "HS256":
		h = sha256.New
	case "HS384":
		h = sha512.New384
	case "HS512":
		h = sha512.New
	default:
		return "", fmt.Errorf("unsupported JWT algorithm %q", alg)
	}
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := enc.EncodeToString(head) + "." + enc.EncodeToString(body)
	m := hmac.New(h, []byte(secret))
	m.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(m.Sum(nil)), nil
}

// webdavFiles are what webdav-sync.js download() fetches.
var webdavFiles = []string{
	"settings.json", "bookmarks.json", "bookmarkGroups.json", "terminalThemes.json", "quickCommands.json",
	"profiles.json", "addressBookmarks.json", "workspaces.json", "userConfig.json", "electerm-status.json",
	"settings.order.json", "bookmarks.order.json", "bookmarkGroups.order.json", "terminalThemes.order.json",
	"quickCommands.order.json", "profiles.order.json", "addressBookmarks.order.json", "workspaces.order.json",
}

// webdavSync is webdav-sync.js; token is "serverUrl####username####password####skipVerify".
func webdavSync(fn string, args []any, token, proxy string) (any, error) {
	parts := append(strings.Split(token, "####"), "", "", "", "")
	base, user, pass, skip := strings.TrimRight(parts[0], "/"), parts[1], parts[2], parts[3] == "true"
	if base == "" {
		return nil, fmt.Errorf("WebDAV server URL is not configured")
	}
	c, err := httpClient(proxy, skip, 60*time.Second)
	if err != nil {
		return nil, err
	}
	do := func(method, p string, body string, extra map[string]string) (int, string, error) {
		req, err := http.NewRequest(method, base+p, strings.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		req.SetBasicAuth(user, pass)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), err
	}
	switch fn {
	case "test":
		st, body, err := do("PROPFIND", "/", "", map[string]string{"Depth": "0"})
		if err != nil {
			return nil, err
		}
		if st == 207 || st == 200 {
			return map[string]any{"success": true, "status": st}, nil
		}
		return nil, fmt.Errorf("WebDAV server returned %d: %s", st, body)
	case "upload":
		st, body, err := do("MKCOL", "/electerm", "", nil)
		if err != nil {
			return nil, err
		}
		if st != 201 && st != 405 && st != 200 {
			return nil, fmt.Errorf("MKCOL /electerm returned %d: %s", st, body)
		}
		for name, content := range asMap(arg0(args)) {
			s, ok := content.(string)
			if !ok {
				b, _ := json.Marshal(content)
				s = string(b)
			}
			st, body, err := do("PUT", "/electerm/"+name, s, nil)
			if err != nil {
				return nil, err
			}
			if st < 200 || st >= 300 {
				return nil, fmt.Errorf("Failed to upload %s: PUT returned %d: %s", name, st, body)
			}
		}
		return map[string]any{"success": true}, nil
	case "download":
		files := map[string]any{}
		for _, name := range webdavFiles {
			st, body, err := do("GET", "/electerm/"+name, "", nil)
			if err != nil {
				return nil, err
			}
			if st == 404 {
				continue
			}
			if st < 200 || st >= 300 {
				return nil, fmt.Errorf("GET /electerm/%s returned %d: %s", name, st, body)
			}
			files[name] = map[string]any{"content": body}
		}
		return map[string]any{"files": files}, nil
	}
	return nil, fmt.Errorf("Unknown WebDAV function: %s", fn)
}

// githubClientID is the wailsterm OAuth App; device flow needs no client secret.
const githubClientID = "Ov23liuBXezooraoMVN3"

var githubOAuthBase = "https://github.com"

func githubOAuthPost(path string, form url.Values, proxy string) (map[string]any, error) {
	c, err := httpClient(proxy, false, 30*time.Second)
	if err != nil {
		return nil, err
	}
	form.Set("client_id", githubClientID)
	req, err := http.NewRequest("POST", githubOAuthBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("GitHub returned %d: %w", resp.StatusCode, err)
	}
	return m, nil
}

// githubDeviceCode starts GitHub's device flow for a gist-scoped token:
// {device_code, user_code, verification_uri, interval, expires_in}.
func githubDeviceCode(proxy string) (any, error) {
	m, err := githubOAuthPost("/login/device/code", url.Values{"scope": {"gist"}}, proxy)
	if err != nil {
		return nil, err
	}
	if e, ok := m["error"].(string); ok {
		return nil, fmt.Errorf("GitHub: %s %v", e, m["error_description"])
	}
	return m, nil
}

// githubDeviceToken polls until the user approves the code in the browser, then returns the access token.
func githubDeviceToken(deviceCode string, interval, expiresIn int, proxy string) (any, error) {
	wait := time.Duration(max(interval, 5)) * time.Second
	deadline := time.Now().Add(time.Duration(max(expiresIn, 60)) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(wait)
		m, err := githubOAuthPost("/login/oauth/access_token", url.Values{
			"device_code": {deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		}, proxy)
		if err != nil {
			return nil, err
		}
		if t, ok := m["access_token"].(string); ok && t != "" {
			return t, nil
		}
		switch m["error"] {
		case "authorization_pending":
		case "slow_down":
			wait += 5 * time.Second
		default:
			return nil, fmt.Errorf("GitHub: %v %v", m["error"], m["error_description"])
		}
	}
	return nil, fmt.Errorf("GitHub login code expired")
}
