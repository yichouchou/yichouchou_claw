package localcommand

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeSessionStore 模拟 eino 的 session storage。
// 验证 ResolveAuthorization 能正确跨 ctx 边界读到 session 中存储的 AuthorizationScope。
type fakeSessionStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func (f *fakeSessionStore) get(key string) (any, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.values[key]
	return v, ok
}

func (f *fakeSessionStore) set(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = make(map[string]any)
	}
	f.values[key] = value
}

// TestResolveAuthorization_SessionFallback 验证：ctx 中无授权时，从 session fallback。
func TestResolveAuthorization_SessionFallback(t *testing.T) {
	store := &fakeSessionStore{}
	RegisterSessionOps(
		func(_ context.Context, key string, value any) { store.set(key, value) },
		func(_ context.Context, key string) (any, bool) { return store.get(key) },
	)
	defer RegisterSessionOps(nil, nil) // 还原

	auth := AuthorizationScope{
		Install:   true,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		GrantedBy: "user",
	}

	ctx := context.Background()

	// 初始状态：ctx 和 session 都没有授权 → 零值
	got := ResolveAuthorization(ctx)
	if !got.IsEmpty() {
		t.Fatalf("ResolveAuthorization(before write) = %+v, want empty", got)
	}

	// 写入 session
	WriteAuthorizationToSession(ctx, auth)

	// 从同一个 ctx 读：应该拿到授权
	got = ResolveAuthorization(ctx)
	if got.IsEmpty() {
		t.Fatalf("ResolveAuthorization(after session write) = empty, want %+v", auth)
	}
	if !got.Install {
		t.Fatalf("ResolveAuthorization().Install = false, want true")
	}
	if got.GrantedBy != "user" {
		t.Fatalf("ResolveAuthorization().GrantedBy = %q, want %q", got.GrantedBy, "user")
	}

	// ctx 中的 AuthorizationScope 优先级应该高于 session
	ctxAuth := AuthorizationScope{
		Bash:      true,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		GrantedBy: "ctx",
	}
	ctxWithAuth := WithAuthorization(ctx, ctxAuth)
	got = ResolveAuthorization(ctxWithAuth)
	if !got.Bash {
		t.Fatalf("ResolveAuthorization(ctx with auth).Bash = false, want true")
	}
	if got.GrantedBy != "ctx" {
		t.Fatalf("ResolveAuthorization(ctx with auth).GrantedBy = %q, want %q", got.GrantedBy, "ctx")
	}
}

// TestResolveAuthorization_ExpiredSession 验证：session 中授权过期时返回零值。
func TestResolveAuthorization_ExpiredSession(t *testing.T) {
	store := &fakeSessionStore{}
	RegisterSessionOps(
		func(_ context.Context, key string, value any) { store.set(key, value) },
		func(_ context.Context, key string) (any, bool) { return store.get(key) },
	)
	defer RegisterSessionOps(nil, nil)

	// 设置过期授权
	expiredAuth := AuthorizationScope{
		Install:   true,
		ExpiresAt: time.Now().Add(-1 * time.Minute), // 已过期
		GrantedBy: "user",
	}
	ctx := context.Background()
	WriteAuthorizationToSession(ctx, expiredAuth)

	got := ResolveAuthorization(ctx)
	if !got.IsEmpty() {
		t.Fatalf("ResolveAuthorization(expired) = %+v, want empty", got)
	}
}

// TestResolveAuthorization_EmptySession 验证：session 中不存在授权时返回零值。
func TestResolveAuthorization_EmptySession(t *testing.T) {
	store := &fakeSessionStore{}
	RegisterSessionOps(
		func(_ context.Context, key string, value any) { store.set(key, value) },
		func(_ context.Context, key string) (any, bool) { return store.get(key) },
	)
	defer RegisterSessionOps(nil, nil)

	ctx := context.Background()
	got := ResolveAuthorization(ctx)
	if !got.IsEmpty() {
		t.Fatalf("ResolveAuthorization(no session value) = %+v, want empty", got)
	}
}
