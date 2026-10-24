package providers

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// ErrRedirectProhibited 表示 HTTP 重定向被禁止（Fix 2b）。
// 重定向不消耗额外 attempts，但可能被利用绕过预算，因此显式禁止。
var ErrRedirectProhibited = errors.New("providers: HTTP redirect prohibited by budget policy")

// Transport 是统一的 HTTP 传输层（stage2-plan §6.4）。
//
// 设计决定：统一 transport 提供共享的 *http.Client 与超时，但“统一 transport
// 不等于同一种请求协议”——Chat/Embedding 走 OpenAI 兼容协议、Rerank 可能走
// DashScope 原生协议，各协议的 encoder/decoder 在 2B 独立实现，本层只负责传输。
//
// Transport 是可注入的：测试可注入自定义 *http.Client（如 httptest.Server.Client()
// 或带自定义 RoundTripper 的 client），生产可共享同一实例以复用连接池。
// 构造 Transport 不发起任何网络请求。
type Transport struct {
	client  *http.Client
	timeout time.Duration
}

// TransportOption 配置 Transport。
type TransportOption func(*transportConfig)

type transportConfig struct {
	client              *http.Client
	timeout             time.Duration
	maxIdleConns        int
	maxIdleConnsPerHost int
	maxConnsPerHost     int
	dialTimeout         time.Duration
}

// WithHTTPClient 注入共享的 *http.Client；设置后 timeout/dial 等选项不再单独生效
// （以注入 client 的配置为准），与 Eino 组件 HTTPClient 语义一致。
func WithHTTPClient(c *http.Client) TransportOption {
	return func(cfg *transportConfig) { cfg.client = c }
}

// WithTransportTimeout 设定整体请求超时（仅在未注入 client 时生效）。
func WithTransportTimeout(d time.Duration) TransportOption {
	return func(cfg *transportConfig) { cfg.timeout = d }
}

// WithMaxIdleConns 设定连接池最大空闲连接数（仅在未注入 client 时生效）。
func WithMaxIdleConns(n int) TransportOption {
	return func(cfg *transportConfig) { cfg.maxIdleConns = n }
}

// WithMaxIdleConnsPerHost 设定每 host 最大空闲连接数（仅在未注入 client 时生效）。
func WithMaxIdleConnsPerHost(n int) TransportOption {
	return func(cfg *transportConfig) { cfg.maxIdleConnsPerHost = n }
}

// WithMaxConnsPerHost 设定每 host 最大连接数（0 表示不限；仅在未注入 client 时生效）。
func WithMaxConnsPerHost(n int) TransportOption {
	return func(cfg *transportConfig) { cfg.maxConnsPerHost = n }
}

// WithDialTimeout 设定建连超时（仅在未注入 client 时生效）。
func WithDialTimeout(d time.Duration) TransportOption {
	return func(cfg *transportConfig) { cfg.dialTimeout = d }
}

// 默认传输参数（保守、可复用连接池）。
const (
	defaultTransportTimeout    = 60 * time.Second
	defaultMaxIdleConns        = 32
	defaultMaxIdleConnsPerHost = 8
	defaultDialTimeout         = 10 * time.Second
)

// NewTransport 构造统一 HTTP 传输层。构造过程不发起网络请求。
// 未注入 client 时，按选项构建带连接池与超时的 *http.Client。
func NewTransport(opts ...TransportOption) *Transport {
	cfg := &transportConfig{
		timeout:             defaultTransportTimeout,
		maxIdleConns:        defaultMaxIdleConns,
		maxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		dialTimeout:         defaultDialTimeout,
	}
	for _, o := range opts {
		o(cfg)
	}
	client := cfg.client
	if client == nil {
		dialer := &net.Dialer{Timeout: cfg.dialTimeout, KeepAlive: 30 * time.Second}
		client = &http.Client{
			Timeout: cfg.timeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           dialer.DialContext,
				MaxIdleConns:          cfg.maxIdleConns,
				MaxIdleConnsPerHost:   cfg.maxIdleConnsPerHost,
				MaxConnsPerHost:       cfg.maxConnsPerHost,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
			// Fix 2b: 禁止自动重定向，避免绕过预算计数
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return ErrRedirectProhibited
			},
		}
	}
	return &Transport{client: client, timeout: cfg.timeout}
}

// Client 返回底层共享的 *http.Client，供 Eino 组件（HTTPClient 字段）与
// 2B 的各协议 encoder/decoder 复用同一传输层。
func (t *Transport) Client() *http.Client {
	if t == nil {
		return nil
	}
	return t.client
}

// Timeout 返回配置的超时（信息用途；注入 client 时以其自身 Timeout 为准）。
func (t *Transport) Timeout() time.Duration {
	if t == nil {
		return 0
	}
	return t.timeout
}

// Do 执行 HTTP 请求（便捷委托，便于在传输层统一插桩/日志）。
// 不在此层做重试——重试是 RequestBudget 的唯一所有权（stage2-plan §4.8）。
func (t *Transport) Do(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}

// CloseIdleConnections 释放空闲连接（进程关闭时清理）。
func (t *Transport) CloseIdleConnections() {
	if t != nil && t.client != nil {
		t.client.CloseIdleConnections()
	}
}
