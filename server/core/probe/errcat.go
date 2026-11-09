package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"

	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// 本文件只做两件事：错误**分类**（决定是否受控重试）与证据**脱敏**（决定
// Report.StopReason 能写什么）。两者都不吞掉底层错误：调用方拿到的 error
// 始终保留原始 %w 链，errors.Is/As 的诊断能力不受影响。

// 重试判定的唯一允许集合：只有这些底层传输原因才允许受控重试。
//
// 反例（必须停止，不得重试）：
//   - 重定向被策略禁止（providers.ErrRedirectProhibited）：*url.Error 包装它，
//     若按"任何 url.Error 都重试"处理，会重复命中原端点并额外消耗预算。
//   - HTTP 状态错误：服务端已给出确定答复，重试只会重复计费。
//   - ctx 取消/deadline、预算耗尽、校验链拒绝。

// isRetryableTransportError 判定一次调用失败是否属于"允许重试的底层传输错误"。
func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	// 1) 先排除明确禁止重试的类别（重定向/预算/取消优先于传输层判断）。
	switch {
	case errors.Is(err, providers.ErrRedirectProhibited):
		return false
	case errors.Is(err, providers.ErrBudgetExhausted):
		return false
	case errors.Is(err, context.Canceled):
		return false
	case errors.Is(err, context.DeadlineExceeded):
		return false
	}
	// 2) HTTP 状态错误是服务端已答复的结果，不属于传输失败。
	if _, ok := httpStatusFromError(err); ok {
		return false
	}
	// 3) 只有 *url.Error 包裹的底层网络原因（或裸 EOF）才进入允许集合。
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	}
	return isRetryableTransportCause(urlErr.Err)
}

// isRetryableTransportCause 只接受连接级中断：EOF、连接重置/中止、管道断开、
// 拨号或读写超时。任何策略类错误（含重定向）一律不重试。
func isRetryableTransportCause(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, providers.ErrRedirectProhibited) ||
		errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
		return true
	}
	// 连接重置/中止/超时在 Go 中都以 net.Error（多数是 *net.OpError）暴露。
	var netErr net.Error
	if errors.As(cause, &netErr) {
		return true
	}
	return false
}

// isTransportFailure 判断错误是否属于传输层失败（用于分类报告，不决定重试）。
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// 证据用的错误类别。它们是固定字符串，不含任何服务端正文、URL、查询参数或凭据。
const (
	categoryCanceled    = "canceled"
	categoryDeadline    = "deadline"
	categoryBudget      = "budget_exhausted"
	categoryRedirect    = "redirect_prohibited"
	categoryHTTPStatus  = "http_status"
	categoryTransport   = "transport"
	categoryIdentity    = "identity_mismatch"
	categoryProfile     = "embedding_profile_not_ready"
	categoryGuard       = "resource_replaced"
	categoryTarget      = "target_binding_not_ready"
	categoryDimension   = "dimension_mismatch"
	categoryAuth        = "not_authorized"
	categoryScope       = "scope_not_supported"
	categoryCredentials = "missing_credentials"
	categoryModelID     = "model_identity_invalid"
	categoryModelVer    = "model_version_not_expressible"
	categoryValidation  = "validation"
	categoryStopped     = "already_stopped"
	categoryUnexpected  = "unexpected"
)

// sanitizeStopReason 把错误折叠成可写入证据的安全摘要。
//
// 允许写入的内容只有：固定类别名，以及（能可靠提取时）HTTP 状态码。
// 绝不写入 err.Error()：服务端错误正文可能回显请求内容或凭据。
func sanitizeStopReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "category=" + categoryCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return "category=" + categoryDeadline
	case errors.Is(err, providers.ErrBudgetExhausted):
		return "category=" + categoryBudget
	case errors.Is(err, providers.ErrRedirectProhibited):
		return "category=" + categoryRedirect
	case errors.Is(err, common.ErrEmbeddingIdentityMismatch):
		return "category=" + categoryIdentity
	case errors.Is(err, common.ErrEmbeddingProfileNotReady):
		return "category=" + categoryProfile
	case errors.Is(err, vector.ErrResourceReplaced):
		return "category=" + categoryGuard
	case errors.Is(err, vector.ErrResourceBindingNotReady):
		return "category=" + categoryTarget
	case errors.Is(err, config.ErrEmbeddingDimensions), errors.Is(err, config.ErrEmbeddingDimensionConflict):
		return "category=" + categoryDimension
	case errors.Is(err, ErrAlreadyStopped):
		return "category=" + categoryStopped
	case errors.Is(err, ErrNotAuthorized):
		return "category=" + categoryAuth
	case errors.Is(err, ErrScopeNotSupported):
		return "category=" + categoryScope
	case errors.Is(err, ErrMissingCredentials):
		return "category=" + categoryCredentials
	case errors.Is(err, providers.ErrModelVersionNotExpressible):
		// 身份里的 @version 无法在 wire 上表达：属于可在预检期确定的非法配置。
		return "category=" + categoryModelVer
	case errors.Is(err, providers.ErrModelIDInvalid):
		return "category=" + categoryModelID
	case errors.Is(err, ErrNotConfigured),
		errors.Is(err, ErrBudgetUnset),
		errors.Is(err, ErrBudgetInvalid),
		errors.Is(err, ErrRetryUnset),
		errors.Is(err, ErrEmptyStep),
		errors.Is(err, ErrTargetRequired),
		errors.Is(err, ErrRemoteNotAllowed),
		errors.Is(err, ErrPlaintextRemote),
		errors.Is(err, ErrEndpointUserInfo):
		return "category=" + categoryValidation
	}
	if code, ok := httpStatusFromError(err); ok {
		return fmt.Sprintf("category=%s status=%d", categoryHTTPStatus, code)
	}
	if isTransportFailure(err) {
		return "category=" + categoryTransport
	}
	return "category=" + categoryUnexpected
}

// ClassifyError 是 sanitizeStopReason 的导出视图，供测试与复核脚本核对
// "同一个错误既保留 errors.Is 链、又只输出安全摘要"的行为。
func ClassifyError(err error) string { return sanitizeStopReason(err) }

// httpStatusFromError 从错误链中提取 HTTP 状态码（若存在）。
//
// 为什么用反射而不是类型断言：状态错误由 eino/OpenAI SDK 产生，其类型是
// 传递依赖（go.mod 中为 indirect）。为提取一个整数而把内部 SDK 提升为直接
// 依赖不值得；这里只读结构体上名为 HTTPStatusCode 的 int 字段（SDK 的
// APIError 与 RequestError 都有该导出字段），不读消息正文。
func httpStatusFromError(err error) (int, bool) {
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if code, ok := httpStatusCodeOf(cur); ok {
			return code, true
		}
	}
	return 0, false
}

func httpStatusCodeOf(v any) (int, bool) {
	return httpStatusCodeOfValue(reflect.ValueOf(v))
}

func httpStatusCodeOfValue(rv reflect.Value) (int, bool) {
	if !rv.IsValid() {
		return 0, false
	}
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return 0, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0, false
	}
	field := rv.FieldByName("HTTPStatusCode")
	if !field.IsValid() || !field.CanInt() {
		return 0, false
	}
	code := int(field.Int())
	if code < 100 || code > 599 {
		return 0, false
	}
	return code, true
}
