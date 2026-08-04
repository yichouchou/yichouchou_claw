package attachment

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegisterAndLookup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// 准备测试文件
	src := filepath.Join(dir, "test.png")
	if err := os.WriteFile(src, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	tok, publicURL, absoluteURL, err := store.Register(src, "image/png", "test.png")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(tok) != 32 {
		t.Errorf("token length = %d, want 32", len(tok))
	}
	if !strings.HasPrefix(publicURL, PublicURLPrefix+"/") {
		t.Errorf("publicURL = %q, missing prefix", publicURL)
	}
	// 配置 baseURL 前,absoluteURL 与 publicURL 相同
	if absoluteURL != publicURL {
		t.Errorf("baseURL unset: absoluteURL = %q, want = %q", absoluteURL, publicURL)
	}

	// 配置 baseURL 后,absoluteURL 变成完整 URL
	store.SetBaseURL("http://localhost:28080")
	_, _, absoluteURL2, err := store.Register(src, "image/png", "test2.png")
	if err != nil {
		t.Fatalf("Register 2: %v", err)
	}
	if !strings.HasPrefix(absoluteURL2, "http://localhost:28080"+PublicURLPrefix+"/") {
		t.Errorf("absoluteURL2 = %q, missing baseURL prefix", absoluteURL2)
	}

	// Lookup 找到
	att := store.Lookup(tok)
	if att == nil {
		t.Fatal("Lookup returned nil")
	}
	if att.Name != "test.png" {
		t.Errorf("Name = %q, want test.png", att.Name)
	}
	if att.Mime != "image/png" {
		t.Errorf("Mime = %q, want image/png", att.Mime)
	}
	if att.Size != int64(len("fake-png-bytes")) {
		t.Errorf("Size = %d, want %d", att.Size, len("fake-png-bytes"))
	}
}

func TestRegister_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	src := filepath.Join(dir, "test.png")
	os.WriteFile(src, []byte("x"), 0o644)

	// 试图用 "../../etc/passwd" 这种文件名
	_, _, _, err = store.Register(src, "application/octet-stream", "../../../etc/passwd")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 验证磁盘上被剥成 "passwd" 而非 "../../etc/passwd"
	att := store.LookupAny()
	if att == nil {
		t.Fatal("no attachment")
	}
	if strings.Contains(att.Name, "..") || strings.Contains(att.Name, "/") {
		t.Errorf("Name = %q, expected cleaned path", att.Name)
	}
}

func (s *Store) LookupAny() *Attachment {
	var out *Attachment
	s.index.Range(func(_, v any) bool {
		out = v.(*Attachment)
		return false
	})
	return out
}

func TestServeHTTPStd(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	src := filepath.Join(dir, "hello.png")
	os.WriteFile(src, []byte("\x89PNG\r\n\x1a\n...rest"), 0o644)

	tok, _, _, err := store.Register(src, "image/png", "hello.png")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 用 httptest 调 ServeHTTPStd
	req := httptest.NewRequest(http.MethodGet,
		PublicURLPrefix+"/"+tok+"/hello.png", nil)
	w := httptest.NewRecorder()
	store.ServeHTTPStd(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	got, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(got), "PNG") {
		t.Errorf("body = %q, expected PNG magic", got)
	}
}

func TestServeHTTPStd_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet,
		PublicURLPrefix+"/badtoken123/file.png", nil)
	w := httptest.NewRecorder()
	store.ServeHTTPStd(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGC(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	src := filepath.Join(dir, "x.png")
	os.WriteFile(src, []byte("x"), 0o644)

	// 旧时间戳注册
	tok, _, _, _ := store.Register(src, "image/png", "x.png")
	att := store.Lookup(tok)
	att.CreatedAt = time.Now().Add(-48 * time.Hour) // 两天前

	cleaned := store.GC(24 * time.Hour)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1", cleaned)
	}
	if store.Lookup(tok) != nil {
		t.Error("Lookup should return nil after GC")
	}
}

func TestCtx(t *testing.T) {
	store := MustNewStore(t.TempDir())
	ctx := WithStore(context.Background(), store)
	if got := GetFromCtx(ctx); got != store {
		t.Error("GetFromCtx should return the same store")
	}
	if got := GetFromCtx(context.Background()); got != nil {
		t.Error("GetFromCtx on empty ctx should return nil")
	}
}
