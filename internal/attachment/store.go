// Package attachment 提供分布式附件存储 + HTTP 服务。
//
// 2026-08-03 新增 (Codex CLI 模式移植):
//
//	核心设计: 借鉴 OpenAI Codex CLI 的 view_image 实现 ——
//	  工具产物(/tmp/xxx.png) 不直接 base64 进 LLM 上下文,
//	  而是: 1) 复制到本地存储目录  2) 生成不可猜的 token URL
//	         3) URL 字符串进 LLM 上下文(LLM 看到 URL)
//	         4) 浏览器用 <img src="/api/attachment/{token}/..."> 直接 fetch
//
//	与 Codex 的差异:
//	  - Codex 是 CLI 同进程(localhost:random_port),我们是 HTTP 服务(任意浏览器跨网络访问)
//	  - Codex 进程退出即清理,我们按 TTL 过期清理(支持长时间会话)
//
//	为什么 LLM 上下文不放 base64:
//	  - 节省 token (300KB 图 → 几百字节 URL)
//	  - Ark/GPT 都支持 URL 形式多模态输入
//	  - 任何 provider 都不需要兼容巨大 base64
//	  - 前端 fetch 真正二进制,所见即所得
//
//	落地路径:
//	  1. main.go 启动时 NewStore + 挂 HTTP 路由 /api/attachment/:token/*filepath
//	  2. localcommand 工具 wrapper 通过 ctx 拿到 Store,Register 产出文件
//	  3. 工具结果 ToolResult 文本写 URL + 元数据(LLM 看到 URL + 路径 + mime + size)
//	  4. SSE attachment event 推 URL 给前端(前端 <img src=URL>)
//	  5. 浏览器自动 fetch URL → 后端 serve 文件
//	  6. Store GC 定期清理过期附件
package attachment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// DefaultTTL 附件默认有效期。
const DefaultTTL = 24 * time.Hour

// DefaultRoot 默认附件存储根目录。
const DefaultRoot = "/tmp/yichouchou_claw-attachments"

// PublicURLPrefix 是 HTTP 路由前缀,前端用这个前缀拼 URL。
const PublicURLPrefix = "/api/attachment"

// Attachment 单个附件的元数据。
type Attachment struct {
	Token     string    // 不可猜的访问 token
	Path      string    // 磁盘路径(在 root 内)
	Original  string    // 原始绝对路径(用户文件,可能不在 root 内)
	Mime      string    // MIME type
	Name      string    // 用户文件名
	Size      int64     // 文件大小
	CreatedAt time.Time // 注册时间
}

// Store 内存索引 + 磁盘存储。
//
// 索引结构: sync.Map[token] *Attachment
// 磁盘结构: root/<token>/<original-name>
//   - token 目录隔离,避免 flat 目录文件数爆炸
//   - original-name 保留,让浏览器能拿到友好的下载名
//
// URL 策略 (2026-08-03 修复 Ark 校验):
//   - PublicURL  = "/api/attachment/<token>/<name>"   相对路径, 给前端
//   - AbsoluteURL = "http://host:port/api/attachment/..."  绝对 URL, 给 LLM (Ark 校验)
//   - 浏览器 <img src=PublicURL> 走同源 fetch;LLM 看到的是完整 URL, 符合 Ark 校验
type Store struct {
	root string

	// baseURL 形如 "http://localhost:28080",不含尾斜杠。
	// 如果为空,AbsoluteURL 与 PublicURL 相同(相对路径)。
	baseURL string

	// index 用 sync.Map 因为: 读多写少 + 启动期间少量写 + 不能阻塞 ServeHTTP
	// 不需要 range,按 token 单点 lookup 即可
	index sync.Map

	// stats
	hits   atomic.Int64
	misses atomic.Int64
}

// NewStore 创建一个 Store 并确保 root 目录存在。
func NewStore(root string) (*Store, error) {
	if root == "" {
		root = DefaultRoot
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("attachment: ensure root %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

// SetBaseURL 设置公开的 base URL(主调在 main.go 启动时配置)。
//
//	baseURL 例: "http://localhost:28080"
//	一定要以 "http://" 或 "https://" 开头, 末尾不要斜杠。
func (s *Store) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// BaseURL 返回当前配置的 base URL(末尾无斜杠)。
func (s *Store) BaseURL() string {
	return s.baseURL
}

// Root 返回附件存储的磁盘根目录。
func (s *Store) Root() string { return s.root }

// Register 把磁盘文件 onPath 注册到 Store,复制到 root 下生成新 token。
//
// 返回:
//   - token: 唯一访问 token
//   - publicURL: 浏览器可直接 fetch 的 URL,如 /api/attachment/abc123xxx/flow.png(相对路径)
//   - absoluteURL: 完整 URL, 如 http://localhost:28080/api/attachment/... (Ark 校验用)
//   - error: 复制失败(MIME 探测失败、大小超限、权限问题)
//
// 设计要点:
//   - 复制而非硬链接: 防止原文件被修改/删除后,前端 fetch 拿到残缺内容
//   - 保留文件名: 浏览器看到 `flow.png` 而不是 `abc123xxx.png`
//   - 大小限制: 单文件 ≤ 32MB(防止恶意 larg file 撑爆内存)
//
// 2026-08-03 修复: Ark adapter 拒绝非 http(s):// 或 data:... URL,
//
//	所以 LLM 上下文必须用 absoluteURL(完整 URL),publicURL 留给前端同源 fetch。
func (s *Store) Register(onPath, mime, name string) (token, publicURL, absoluteURL string, err error) {
	const MaxSize = 32 * 1024 * 1024 // 32MB

	info, err := os.Stat(onPath)
	if err != nil {
		return "", "", "", fmt.Errorf("stat: %w", err)
	}
	if info.Size() == 0 {
		return "", "", "", errors.New("attachment: empty file")
	}
	if info.Size() > MaxSize {
		return "", "", "", fmt.Errorf("attachment: file too large (%d > %d)", info.Size(), MaxSize)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	if name == "" {
		name = filepath.Base(onPath)
	}
	// 防路径穿越: 剥掉目录成分
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" {
		name = "file"
	}

	tok, err := genToken()
	if err != nil {
		return "", "", "", fmt.Errorf("gen token: %w", err)
	}

	// 复制到 root/<token>/<name>
	tokenDir := filepath.Join(s.root, tok)
	if err := os.MkdirAll(tokenDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("mkdir token dir: %w", err)
	}
	dst := filepath.Join(tokenDir, name)
	if err := copyFile(onPath, dst); err != nil {
		_ = os.RemoveAll(tokenDir)
		return "", "", "", fmt.Errorf("copy: %w", err)
	}

	att := &Attachment{
		Token:     tok,
		Path:      dst,
		Original:  onPath,
		Mime:      mime,
		Name:      name,
		Size:      info.Size(),
		CreatedAt: time.Now(),
	}
	s.index.Store(tok, att)

	publicURL = fmt.Sprintf("%s/%s/%s", PublicURLPrefix, tok, name)
	if s.baseURL != "" {
		absoluteURL = s.baseURL + publicURL
	} else {
		// 没有 baseURL 时,绝对 URL 就是相对 URL (退化路径 — Ark 会拒绝)
		absoluteURL = publicURL
	}
	return tok, publicURL, absoluteURL, nil
}

// Lookup 按 token 查 attachment。返回 nil 表示不存在。
func (s *Store) Lookup(token string) *Attachment {
	if v, ok := s.index.Load(token); ok {
		return v.(*Attachment)
	}
	return nil
}

// LookupByOriginal 在 Store 里查找原始路径对应的 attachment (2026-08-04 新增)。
//
// 用途: 当 LocalCommandAgent 这一轮 LLM 没有调 local_command 重新生成产物,
// 而是直接 read /tmp/diagrams/xxx.png 并引用 (ROOT_SYSTEM_POLICY 强调
// "产物已注册到 attachment 后严禁重复 base64/再次转码"), 此时 IPC 通道
// 收不到新的 attachment event, 前端看不到图。
//
// 补救: 扫描 LLM 输出文本里的路径,若 Store 里已有 (前几 session 注册过),
//       主动推一次 SSE attachment event 给当前 session,让前端能看到老产物。
//
// 返回第一个匹配的 attachment (Original 精确匹配);未匹配返回 nil。
func (s *Store) LookupByOriginal(originalPath string) *Attachment {
	if originalPath == "" {
		return nil
	}
	var found *Attachment
	s.index.Range(func(_, v any) bool {
		att := v.(*Attachment)
		if att.Original == originalPath {
			found = att
			return false // 找到后停止
		}
		return true
	})
	return found
}

// ListAll 返回 Store 当前所有 attachment 副本 (2026-08-04 新增)。
//
// 用途: 每次新 session 启动时, main.go pushStartupAttachments 主动把 Store
// 里所有已知附件推到当前 session 前端, 用户立刻能看到产物面板。
//
// 返回按 CreatedAt 升序 (老的在前), 方便前端按时间线展示。
func (s *Store) ListAll() []*Attachment {
	var out []*Attachment
	s.index.Range(func(_, v any) bool {
		att := v.(*Attachment)
		// 拷贝一份避免外部修改 (race)
		cp := *att
		out = append(out, &cp)
		return true
	})
	// 简单排序 (列表不大, 几十个量级)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].CreatedAt.After(out[j].CreatedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ServeHTTP hertz 路由处理器: GET /api/attachment/:token/*filepath
//
// 校验: 1) token 存在  2) 请求路径中的文件名 == index 里的 name  3) 文件还在磁盘
// 返回: Content-Type = mime, Content-Disposition 让浏览器 inline 渲染
func (s *Store) ServeHTTP(ctx context.Context, c *app.RequestContext) {
	tok := c.Param("token")
	if tok == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "missing token"})
		return
	}

	att := s.Lookup(tok)
	if att == nil {
		s.misses.Add(1)
		c.JSON(consts.StatusNotFound, map[string]string{"error": "attachment not found or expired"})
		return
	}

	// 路径名校验: 防止路径穿越(c.Param 富文本可能含 `..`)
	reqName := c.Param("filepath")
	if reqName == "" {
		reqName = att.Name
	}
	if filepath.Base(reqName) != att.Name {
		s.misses.Add(1)
		c.JSON(consts.StatusForbidden, map[string]string{"error": "name mismatch"})
		return
	}

	// 校验文件还在磁盘
	if _, err := os.Stat(att.Path); err != nil {
		s.misses.Add(1)
		s.index.Delete(tok)
		c.JSON(consts.StatusNotFound, map[string]string{"error": "file missing on disk"})
		return
	}

	s.hits.Add(1)

	// 设置 Content-Type
	c.Header("Content-Type", att.Mime)
	// inline 而非 attachment: 浏览器直接渲染 (img / pdf / ...)
	c.Header("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", sanitizeFilename(att.Name)))
	// 缓存策略: 5 分钟过期(支持重复访问,但不会永久占用)
	c.Header("Cache-Control", "private, max-age=300")

	// 走 hertz SendFile: 零拷贝,大数据性能高
	c.File(att.Path)
}

// GC 清理超过 ttl 的附件。返回清理数量。
//
// 启动时按一定间隔定时调用;服务退出时也调一次。
func (s *Store) GC(ttl time.Duration) int {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	cutoff := time.Now().Add(-ttl)
	cleaned := 0
	s.index.Range(func(k, v any) bool {
		att := v.(*Attachment)
		if att.CreatedAt.Before(cutoff) {
			if err := os.RemoveAll(filepath.Dir(att.Path)); err == nil {
				s.index.Delete(k)
				cleaned++
			}
		}
		return true
	})
	return cleaned
}

// Close 清理所有附件。服务退出时调用。
func (s *Store) Close() error {
	cleaned := 0
	s.index.Range(func(k, v any) bool {
		att := v.(*Attachment)
		if err := os.RemoveAll(filepath.Dir(att.Path)); err == nil {
			cleaned++
		}
		s.index.Delete(k)
		return true
	})
	_ = os.RemoveAll(s.root)
	return nil
}

// Stats 返回运行时统计。
func (s *Store) Stats() (hits, misses int64, total int) {
	return s.hits.Load(), s.misses.Load(), s.totalCount()
}

func (s *Store) totalCount() int {
	n := 0
	s.index.Range(func(_, _ any) bool { n++; return true })
	return n
}

// === 内部辅助 ===

// genToken 生成 16 字节(32 hex字符)的不可猜 token。
func genToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// copyFile 跨文件系统安全复制,避免用 os.Link(跨 fs 失败)。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// sanitizeFilename 去掉 ASCII 控制字符 + 双引号,避免 HTTP 头注入。
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		out = "file"
	}
	return out
}

// === ctx 注入 ===

// ctxKey 是 ctx 里 attachment.Store 的 key,避免和别的包冲突。
type ctxKey struct{}

// WithStore 把 Store 注入 ctx。
func WithStore(ctx context.Context, s *Store) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// GetFromCtx 从 ctx 取 Store。返回 nil 表示没注入。
func GetFromCtx(ctx context.Context) *Store {
	if s, ok := ctx.Value(ctxKey{}).(*Store); ok {
		return s
	}
	return nil
}

// --- helpers for testing ---

// MustNewStore 是 NewStore 的 panic 版本,仅用于测试/启动初始化。
func MustNewStore(root string) *Store {
	s, err := NewStore(root)
	if err != nil {
		panic(err)
	}
	return s
}

// ServeHTTPStd 是 *http.Handler 兼容版本,用于标准 http.Handler 测试。
func (s *Store) ServeHTTPStd(w http.ResponseWriter, r *http.Request) {
	// 提取 token 和 filepath
	rest := strings.TrimPrefix(r.URL.Path, PublicURLPrefix+"/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	tok, name := parts[0], parts[1]
	att := s.Lookup(tok)
	if att == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if filepath.Base(name) != att.Name {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", att.Mime)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", sanitizeFilename(att.Name)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, att.Path)
}
