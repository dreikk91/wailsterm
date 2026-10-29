package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
)

func TestWebDAVSync(t *testing.T) {
	dav := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		dav.ServeHTTP(w, r)
	}))
	defer srv.Close()
	token := srv.URL + "####me####pw####false"

	if r, err := doSync("webdav", "test", nil, token, ""); err != nil || asMap(r)["success"] != true {
		t.Fatalf("test = %v, %v", r, err)
	}
	if _, err := doSync("webdav", "test", nil, srv.URL+"####me####wrong", ""); err == nil {
		t.Fatal("bad password accepted")
	}
	up := map[string]any{"bookmarks.json": `[{"id":"a"}]`, "userConfig.json": map[string]any{"theme": "dark"}}
	if _, err := doSync("webdav", "upload", []any{up}, token, ""); err != nil {
		t.Fatal(err)
	}
	r, err := doSync("webdav", "download", nil, token, "")
	if err != nil {
		t.Fatal(err)
	}
	files := asMap(asMap(r)["files"])
	if asMap(files["bookmarks.json"])["content"] != `[{"id":"a"}]` || asMap(files["userConfig.json"])["content"] != `{"theme":"dark"}` || len(files) != 2 {
		t.Fatalf("download = %v", files)
	}
}

func TestCustomSync(t *testing.T) {
	var got []string
	stored := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(auth, ".")
		if len(parts) != 3 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		m := hmac.New(sha256.New, []byte("secret"))
		m.Write([]byte(parts[0] + "." + parts[1]))
		if base64.RawURLEncoding.EncodeToString(m.Sum(nil)) != parts[2] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		json.Unmarshal(claims, &c)
		got = append(got, r.Method+" "+c["id"].(string))
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			stored = string(b)
			w.Write([]byte(`{"ok":true}`))
		case "GET":
			w.Write([]byte(stored))
		default:
			w.Write([]byte(`"ok"`))
		}
	}))
	defer srv.Close()
	token := "secret####" + srv.URL + "####user1####HS256"
	if _, err := doSync("custom", "test", []any{"user1"}, token, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := doSync("custom", "update", []any{"user1", map[string]any{"files": map[string]any{"a": 1}}}, token, ""); err != nil {
		t.Fatal(err)
	}
	r, err := doSync("custom", "getOne", []any{"user1"}, token, "")
	if err != nil || asMap(asMap(r)["files"])["a"] != float64(1) {
		t.Fatalf("getOne = %v, %v", r, err)
	}
	if strings.Join(got, ",") != "POST user1,PUT user1,GET user1" {
		t.Fatalf("requests = %v", got)
	}
	// errors read like electerm-sync's HTTPError
	_, err = doSync("custom", "test", nil, "wrong####"+srv.URL+"####u", "")
	if err == nil || !strings.HasPrefix(err.Error(), "status: 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncEncryptionMatchesElecterm(t *testing.T) {
	const pass, plain = "pw-тест", "hello синхронізація"
	// produced by Node's crypto exactly as electerm's lib/enc.js does
	gcmVec := "gcm:010101010101010101010101:02020202020202020202020202020202:cb58ee22b0b3883c42243a25aea2c76b:41ee61f6f637cec8a668d9ed8cebff82d6d6baa4a12f03d94f90093ccd8ca71e"
	legacyVec := "34c27f59383f98c79c181ef330a0f4b783bd9586076b6518d4d5f3798703501b54eaeff6c349b63549730ed22e8cddd3"
	for _, v := range []string{gcmVec, legacyVec} {
		if got, err := decryptSync(v, pass); err != nil || got != plain {
			t.Fatalf("decrypt(%s...) = %q, %v", v[:12], got, err)
		}
	}
	enc, err := encryptSync(plain, pass)
	if err != nil || !strings.HasPrefix(enc, "gcm:") {
		t.Fatal(enc, err)
	}
	if got, _ := decryptSync(enc, pass); got != plain {
		t.Fatalf("round trip = %q", got)
	}
	if _, err := decryptSync(enc, "wrong"); err == nil {
		t.Fatal("wrong password decrypted")
	}
}

func TestGithubDeviceFlow(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != githubClientID {
			t.Errorf("client_id = %q", r.Form.Get("client_id"))
		}
		switch r.URL.Path {
		case "/login/device/code":
			io.WriteString(w, `{"device_code":"dc","user_code":"AB-CD","verification_uri":"https://github.com/login/device","interval":0,"expires_in":900}`)
		case "/login/oauth/access_token":
			if r.Form.Get("device_code") != "dc" {
				t.Errorf("device_code = %q", r.Form.Get("device_code"))
			}
			if polls++; polls == 1 {
				io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			io.WriteString(w, `{"access_token":"gho_x","token_type":"bearer","scope":"gist"}`)
		}
	}))
	defer srv.Close()
	githubOAuthBase = srv.URL
	defer func() { githubOAuthBase = "https://github.com" }()

	code, err := githubDeviceCode("")
	if err != nil || asMap(code)["user_code"] != "AB-CD" {
		t.Fatal(code, err)
	}
	// ponytail: real 5s minimum poll interval, so this test takes ~10s
	tok, err := githubDeviceToken("dc", 0, 900, "")
	if err != nil || tok != "gho_x" || polls != 2 {
		t.Fatal(tok, err, polls)
	}
}
