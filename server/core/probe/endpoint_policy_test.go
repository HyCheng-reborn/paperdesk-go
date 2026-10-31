//go:build !online

// 默认构建的远端隔离回归测试（复核 P1）。
//
// 目标不是"默认参数安全"，而是"默认构建里**不存在**可打开远端的入口"：
//   - ChainOptions 的字段集合被断言锁定（任何新增字段都会让本测试失败，
//     强制复核者确认它不是新的远端开关）；
//   - 即使计划携带已批准的授权、即使端点写成 HTTPS，默认构建仍拒绝构造；
//   - 在**进程启动期**（TestMain，早于任何请求）把 HTTP_PROXY/HTTPS_PROXY 指向本机
//     fake 代理，并断言代理命中数为 0，证明拒绝发生在任何发送之前。
//     为什么必须在 TestMain 而不是 t.Setenv：net/http 的 ProxyFromEnvironment 每个进程
//     只解析一次环境变量，测试中途 Setenv 对已经缓存的配置无效，零命中会成为空断言。

package probe_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/wangle201210/go-rag/server/core/probe"
)

// proxyHits 统计所有“本应被回环旁路以外”的外发请求；正常离线测试应恒为 0。
var proxyHits atomic.Int64

// TestMain 在任何测试之前装好本机 fake 代理，让“零外发”可被真实观测。
// 回环地址会被 net/http 直接旁路（useProxy），因此不影响方案 A 的本机 fake 链路。
func TestMain(m *testing.M) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		os.Setenv(name, proxy.URL)
	}
	os.Setenv("NO_PROXY", "")
	os.Setenv("no_proxy", "")

	os.Exit(m.Run())
}

// TestDefaultBuild_ChainOptionsHasNoRemoteSwitch 锁定构造入口的字段集合。
func TestDefaultBuild_ChainOptionsHasNoRemoteSwitch(t *testing.T) {
	if probe.RemoteEndpointsAllowed() {
		t.Fatal("default build must not allow remote endpoints")
	}
	typ := reflect.TypeOf(probe.ChainOptions{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if len(fields) != 1 || fields[0] != "APIKey" {
		t.Fatalf("ChainOptions fields = %v, want exactly [APIKey] (no caller-controlled remote switch)", fields)
	}
}

// TestDefaultBuild_RemoteEndpointsRejectedWithZeroSends 证明远端端点无法进入发送阶段。
func TestDefaultBuild_RemoteEndpointsRejectedWithZeroSends(t *testing.T) {
	before := proxyHits.Load()

	cases := []struct {
		name   string
		mutate func(*probe.Plan)
	}{
		{"plaintext_remote", func(p *probe.Plan) {
			p.Chat.Endpoint = "http://api.example.invalid/v1"
			p.Embed.Endpoint = "http://api.example.invalid/v1"
			p.Rerank.URL = "http://api.example.invalid/rerank"
		}},
		{"https_remote", func(p *probe.Plan) {
			p.Chat.Endpoint = "https://api.example.invalid/v1"
			p.Embed.Endpoint = "https://api.example.invalid/v1"
			p.Rerank.URL = "https://api.example.invalid/rerank"
		}},
		{"approved_authorization_is_not_enough", func(p *probe.Plan) {
			p.Chat.Endpoint = "https://api.example.invalid/v1"
			p.Embed.Endpoint = "https://api.example.invalid/v1"
			p.Rerank.URL = "https://api.example.invalid/rerank"
			// 即使计划携带完整授权，默认构建仍不得具备远端能力。
			p.Authorization = probe.Authorization{
				OnlineApproved: true,
				Scope:          probe.ScopeS1,
				ApprovedBy:     "offline-reviewer",
				ApprovedAt:     "offline",
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			tc.mutate(&plan)
			if err := plan.Validate(); err != nil {
				t.Fatalf("test plan must be valid: %v", err)
			}

			target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
			if err != nil {
				t.Fatalf("new local fake es target: %v", err)
			}
			defer target.Close()

			chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{
				APIKey: "probe-offline-placeholder-key",
			})
			if chain != nil {
				chain.Close()
			}
			if err == nil {
				t.Fatal("NewChain() = nil error, want the remote endpoint to be rejected")
			}
			// 端点策略先于构造：必须是端点类别，而不是别的偶然错误。
			if got := probe.ClassifyError(err); got != "category=validation" {
				t.Fatalf("NewChain() = %v (category %q), want the endpoint-policy validation category", err, got)
			}
		})
	}

	if got := proxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0 (no request may leave the process)", got)
	}
}

// TestProxyControl_EnvironmentIsHonored 是控制组：证明 ProxyFromEnvironment 确实指向
// 本机 fake 代理，因此其它用例的“零命中”不是空断言。
// 请求只到本机代理（.invalid 域名 + 本机代理），不产生互联网访问。
func TestProxyControl_EnvironmentIsHonored(t *testing.T) {
	before := proxyHits.Load()
	req, err := http.NewRequest(http.MethodGet, "http://probe-control.example.invalid/", nil)
	if err != nil {
		t.Fatalf("build control request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("control request did not reach the local fake proxy: %v", err)
	}
	_ = resp.Body.Close()
	if got := proxyHits.Load() - before; got != 1 {
		t.Fatalf("control proxy hits = %d, want 1 (the zero-hit assertions depend on this)", got)
	}
}

// TestDefaultBuild_CheckEndpointsRejectsRemote 直接对端点策略本身断言，
// 便于复核者区分"策略拒绝"与"构造失败"。
func TestDefaultBuild_CheckEndpointsRejectsRemote(t *testing.T) {
	plan := validPlan()
	plan.Chat.Endpoint = "https://api.example.invalid/v1"
	if err := probe.CheckEndpoints(plan); err == nil {
		t.Fatal("CheckEndpoints() = nil, want remote rejection in the default build")
	}

	// 回环端点（本机 fake 形态）仍可用：方案 A 的离线链路不受影响。
	loopback := validPlan()
	if err := probe.CheckEndpoints(loopback); err != nil {
		t.Fatalf("loopback plan must pass the endpoint policy: %v", err)
	}
}
