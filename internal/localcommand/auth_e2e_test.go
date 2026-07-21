package localcommand

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestResolveAuthorization_RealSessionSimulate 模拟 eino session 实际使用场景：
//
//	eino ADK 里 AddSessionValue / GetSessionValue 实际是：
//	  - AddSessionValue: 通过 ctx.Value(runCtxKey{}).(*runContext).Session.addValue(key, value)
//	  - GetSessionValue: 反向取出
//
//	我们的 ReadAuthorizationFromSession 调用 eino.GetSessionValue(ctx, key)
//	我们的 WriteAuthorizationToSession 调用 eino.AddSessionValue(ctx, key, value)
//
//	所以这里直接模拟 eino session 的内部行为，把数据写到类似位置即可。
func TestResolveAuthorization_RealSessionSimulate(t *testing.T) {
	// 模拟 eino runSession.Values 的 map 行为
	sessionValues := make(map[string]any)
	var mu sync.RWMutex

	// 把 eino.AddSessionValue / GetSessionValue 替换为对 sessionValues 的读写
	RegisterSessionOps(
		func(_ context.Context, key string, value any) {
			mu.Lock()
			defer mu.Unlock()
			sessionValues[key] = value
		},
		func(_ context.Context, key string) (any, bool) {
			mu.RLock()
			defer mu.RUnlock()
			v, ok := sessionValues[key]
			return v, ok
		},
	)
	defer RegisterSessionOps(nil, nil)

	auth := AuthorizationScope{
		Install:   true,
		ExpiresAt: time.Now().Add(10 * time.Minute),
		GrantedBy: "test-user",
	}
	ctx := context.Background()

	// 写入 session（模拟 AuthorizationMiddleware 在 ctx+session 都写了）
	WriteAuthorizationToSession(ctx, auth)

	// 验证 ResolveAuthorization 能从 session 读出来
	got := ResolveAuthorization(ctx)
	if got.IsEmpty() {
		t.Fatalf("ResolveAuthorization() returned empty, want %+v", auth)
	}
	if !got.Install {
		t.Fatalf("Install=%v, want true", got.Install)
	}

	// 验证：写入过期的 AuthorizationScope 后 ResolveAuthorization 返回零值
	sessionValues["localcommand.AuthorizationScope"] = AuthorizationScope{
		Install:   true,
		ExpiresAt: time.Now().Add(-1 * time.Minute),
		GrantedBy: "test-user",
	}
	got = ResolveAuthorization(ctx)
	if !got.IsEmpty() {
		t.Fatalf("Expired auth should return empty, got %+v", got)
	}
}
