package providers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// B6: 重定向门禁覆盖注入 Client
//
// 修复前 CheckRedirect 只在默认新建 Client 时设置，注入 Client 路径裸奔。
// 以下测试全部用本机 httptest，断言实际发送数与重定向禁止。
// ---------------------------------------------------------------------------

// TestRedirect_InjectedClientProhibited 验证注入的 *http.Client 也被强制禁止重定向。
func TestRedirect_InjectedClientProhibited(t *testing.T) {
	var targetHits int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirectSrv.Close()

	// 注入的 client 本身不设 CheckRedirect（默认会跟随重定向）。
	injected := redirectSrv.Client()
	if injected.CheckRedirect != nil {
		t.Fatal("test precondition: injected client should not preset CheckRedirect")
	}
	tr := NewTransport(WithHTTPClient(injected))

	_, err := tr.Client().Get(redirectSrv.URL + "/chat")
	if err == nil {
		t.Fatal("expected error due to redirect prohibition on injected client")
	}
	if !errors.Is(err, ErrRedirectProhibited) {
		t.Errorf("expected ErrRedirectProhibited, got: %v", err)
	}
	// 重定向被禁止：目标服务器不应收到任何请求。
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Errorf("expected 0 hits on redirect target, got %d", n)
	}
}

// TestRedirect_AttemptsCounted 验证每次真实发送计一次 attempt：
// 重定向被门禁拦截，只发生一次真实 RoundTrip，不因跟随重定向而多发。
func TestRedirect_AttemptsCounted(t *testing.T) {
	var targetHits int32
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirectSrv.Close()

	var sends int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&sends, 1) // 每次真实发送计数
		return http.DefaultTransport.RoundTrip(req)
	})}
	tr := NewTransport(WithHTTPClient(client))

	_, err := tr.Client().Get(redirectSrv.URL + "/chat")
	if err == nil {
		t.Fatal("expected redirect to be prohibited")
	}

	// 只有初始请求这一次真实发送；重定向未跟随 → 无第二次发送。
	if n := atomic.LoadInt32(&sends); n != 1 {
		t.Errorf("expected exactly 1 real send, got %d", n)
	}
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Errorf("expected 0 hits on redirect target, got %d", n)
	}
}
