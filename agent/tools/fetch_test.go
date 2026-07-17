package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runFetch(in fetchInput) (string, error) {
	b, _ := json.Marshal(in)
	return Fetch().Execute(context.Background(), b)
}

func TestFetchJSON(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	out, err := runFetch(fetchInput{URL: srv.URL, Method: "POST", JSON: json.RawMessage(`{"name":"gus"}`)})
	if err != nil {
		t.Fatal(err, out)
	}
	if gotCT != "application/json" {
		t.Errorf("json helper should set Content-Type, got %q", gotCT)
	}
	if gotBody != `{"name":"gus"}` {
		t.Errorf("json body wrong: %q", gotBody)
	}
	if !strings.Contains(out, "HTTP 200") || !strings.Contains(out, `{"ok":true}`) {
		t.Errorf("output missing status/body:\n%s", out)
	}
}

func TestFetchForm(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()

	_, err := runFetch(fetchInput{URL: srv.URL, Method: "POST", Form: map[string]string{"user": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("form Content-Type wrong: %q", gotCT)
	}
	if gotBody != "user=x" {
		t.Errorf("form body wrong: %q", gotBody)
	}
}

func TestFetchFilesMultipart(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "up.txt")
	os.WriteFile(tmp, []byte("filecontent"), 0644)

	var gotCT string
	var gotField, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		r.ParseMultipartForm(1 << 20)
		gotField = r.FormValue("name")
		f, _, _ := r.FormFile("doc")
		if f != nil {
			b, _ := io.ReadAll(f)
			gotFile = string(b)
		}
	}))
	defer srv.Close()

	// files + form combined (multipart mixto).
	_, err := runFetch(fetchInput{
		URL: srv.URL, Method: "POST",
		Files: []fetchFile{{Field: "doc", Path: tmp}},
		Form:  map[string]string{"name": "gus"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("multipart Content-Type wrong: %q", gotCT)
	}
	if gotField != "gus" {
		t.Errorf("form field in multipart wrong: %q", gotField)
	}
	if gotFile != "filecontent" {
		t.Errorf("uploaded file content wrong: %q", gotFile)
	}
}

func TestFetchMutualExclusion(t *testing.T) {
	_, err := runFetch(fetchInput{URL: "http://x", Body: "raw", JSON: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "only one request body") {
		t.Errorf("expected mutual-exclusion error, got %v", err)
	}
}

func TestFetchErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	out, err := runFetch(fetchInput{URL: srv.URL})
	if err == nil {
		t.Error("4xx should be reported as error")
	}
	if !strings.Contains(out, "HTTP 404") {
		t.Errorf("output should show status: %s", out)
	}
}

func TestFetchResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "val")
		w.WriteHeader(200)
		w.Write([]byte("body"))
	}))
	defer srv.Close()

	out, _ := runFetch(fetchInput{URL: srv.URL})
	if !strings.Contains(out, "X-Custom: val") {
		t.Errorf("response headers should be shown:\n%s", out)
	}
}

func TestFetchDownloadTo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("binary-data"))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.bin")
	out, err := runFetch(fetchInput{URL: srv.URL, DownloadTo: dst})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved") {
		t.Errorf("download output wrong: %s", out)
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "binary-data" {
		t.Errorf("downloaded content wrong: %q", data)
	}
}

func TestFetchRedirectNotError(t *testing.T) {
	// A followed redirect ends at 200 → not an error.
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("landed"))
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, 302)
	}))
	defer redir.Close()

	out, err := runFetch(fetchInput{URL: redir.URL})
	if err != nil {
		t.Errorf("followed redirect should not be an error, got %v", err)
	}
	if !strings.Contains(out, "landed") {
		t.Errorf("should have landed on final page: %s", out)
	}
}

func TestFetch5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	_, err := runFetch(fetchInput{URL: srv.URL})
	if err == nil {
		t.Error("5xx should be an error")
	}
}

func TestFetchDownloadErrorDoesNotSave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("<html>not found</html>"))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "img.png")
	out, err := runFetch(fetchInput{URL: srv.URL, DownloadTo: dst})
	if err == nil {
		t.Error("404 download should be an error")
	}
	// The file must NOT exist (we don't save error pages as the target file).
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("error response must not be saved to the download path")
	}
	// The error output should carry the status + body for diagnosis.
	if !strings.Contains(out, "HTTP 404") || !strings.Contains(out, "not found") {
		t.Errorf("error output should show status+body: %s", out)
	}
}

func TestFetchNoFollowRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(302)
	}))
	defer srv.Close()

	no := false
	out, err := runFetch(fetchInput{URL: srv.URL, FollowRedirects: &no})
	if err != nil {
		t.Fatalf("302 without following is not an error: %v", err)
	}
	if !strings.Contains(out, "HTTP 302") || !strings.Contains(out, "Location: /elsewhere") {
		t.Errorf("should show the 302 + Location header:\n%s", out)
	}
}

func TestFetchHEAD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "1234")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out, err := runFetch(fetchInput{URL: srv.URL, Method: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 200") {
		t.Errorf("HEAD should work: %s", out)
	}
}

func TestFetchCustomTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("slow"))
	}))
	defer srv.Close()

	// timeout=1s should allow the 500ms response.
	_, err := runFetch(fetchInput{URL: srv.URL, Timeout: 1})
	if err != nil {
		t.Errorf("1s timeout should allow a 500ms response: %v", err)
	}
}
