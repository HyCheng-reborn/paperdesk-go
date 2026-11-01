// Package probe 是 Stage 2D 在线探针（S-1 方案 A）的离线骨架与预算接线验收入口。
//
// 边界（对应 design/stage2d-plan.md §2.1 方案 A、§3.2.2 W-1..W-5）：
//   - 默认构建不发起任何真实在线请求，且**不存在可绕过远端隔离的调用入口**：
//     默认构建（!online）只接受回环端点（remoteEndpointsAllowed=false，见
//     endpoint_policy.go），ChainOptions 不含任何远端开关；远端能力只在 online 构建中
//     存在（endpoint_policy_online.go），并要求非回环端点使用 HTTPS、显式授权与凭据
//     环境变量存在。
//   - 每次执行的完整预检（计划 + 端点策略 + 全部步骤）在第一步发送前一次完成
//     （Preflight，见 preflight.go），与 Readiness 共用同一实现，两者不会给出相反结论。
//   - Embedding 侧走生产链：vector.BindPhysicalTarget 对可读目标产出真实 TargetBinding →
//     common.NewEmbedding 的 Registry/身份/guard 校验 → guard 在每次 embedding 前后重新
//     读取目标。本机 fake ES 只证明这些校验与读取被真实执行，不证明任何真实 ES 事实。
//   - 一次运行只有一个停止标记：beginAttempt 只取得**入口准入**（登记在途句柄），
//     真正“字节是否发出”由 sendGate 在 HTTP RoundTrip 之前判定（见 sendgate.go）。
//     因此“停止标记之后零新发送/新重试”是可验证事实：已准入但未到达闸门的尝试
//     返回 ErrAlreadyStopped，零字节发出；已交出的请求不声称可撤销，只取消其 ctx。
//   - Chat/Rerank 侧保持 providers.Build*Role 的 adapter 生产路径；本机 httptest 服务只
//     替身 HTTP 对端，不替身预算、guard、身份比较或响应校验代码。
package probe

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/vector"
)

// 范围常量与 design/stage2d-plan.md §2 的 S-1/S-2/S-3 对应。
// 本骨架只实现 S-1 方案 A（本机 fake ES 绑定 + 真实模型端点）。
const (
	ScopeS1 = "S-1"
	ScopeS2 = "S-2"
	ScopeS3 = "S-3"
)

// 探针错误哨兵。所有"待配置/未授权/未设置"一律显式失败，不静默降级为不限或放行。
var (
	// ErrNotConfigured 固定目标/参数未配置（对应草案 U-1/U-2/U-3/U-6 待决项）。
	ErrNotConfigured = errors.New("probe: fixed target or parameters are not configured")
	// ErrNotAuthorized 未获在线执行授权（草案 §2 通用前置条件 A）。
	ErrNotAuthorized = errors.New("probe: online execution is not authorized")
	// ErrScopeNotSupported 本骨架只覆盖 S-1 方案 A，不接受其它授权范围。
	ErrScopeNotSupported = errors.New("probe: skeleton only supports S-1 (local fake ES target + model endpoint)")
	// ErrRemoteNotAllowed 默认构建不接受非回环端点（没有调用方开关可以打开）。
	ErrRemoteNotAllowed = errors.New("probe: non-loopback endpoint is not available in this build")
	// ErrPlaintextRemote 在线构建中非回环端点必须使用 HTTPS（明文会泄露凭据）。
	ErrPlaintextRemote = errors.New("probe: non-loopback endpoint must use https")
	// ErrEndpointUserInfo 端点不得内嵌用户名/密码（凭据只能来自环境变量）。
	ErrEndpointUserInfo = errors.New("probe: endpoint must not embed user info")
	// ErrBudgetUnset 预算维度未设置；0 不是有限上限（草案 §3.2/§3.2.1）。
	ErrBudgetUnset = errors.New("probe: budget dimension is not set (0 is not a finite limit)")
	// ErrBudgetInvalid 预算维度为负或声明了未知的"不限"维度。
	ErrBudgetInvalid = errors.New("probe: invalid budget plan")
	// ErrRetryUnset 重试策略未配置；0 不表示不限。
	ErrRetryUnset = errors.New("probe: retry policy is not configured (0 is not 'unlimited')")
	// ErrAlreadyStopped 探针已因预留失败/非预期错误停止，拒绝启动后续步骤（W-5）。
	ErrAlreadyStopped = errors.New("probe: run already stopped, refusing to start another step")
	// ErrTargetRequired 缺少可验证的物理目标绑定（生产 embedding 入口的硬前提）。
	ErrTargetRequired = errors.New("probe: a verifiable physical target is required for the embedding chain")
	// ErrEmptyStep 探针步骤缺少输入。
	ErrEmptyStep = errors.New("probe: empty probe step")
	// ErrMissingCredentials 所需凭据环境变量不存在（只报告名称，不读取/回显值）。
	ErrMissingCredentials = errors.New("probe: required credential environment variable is not set")
)

// TargetSpec 是固定目标声明（方案 A 下由本机 fake ES 承载；D-3 一次性命名不复用）。
type TargetSpec struct {
	// IndexName 是一次性受控物理名，必须满足 vector.ValidatePhysicalName。
	IndexName string
	// ProviderName 必须与 Registry 的 provider 名一致（身份比较使用同一规范化语义）。
	ProviderName string
	// Similarity 参与 fingerprint，必须与本机 fake ES 的 mapping 一致（如 cosine/dot/euclidean）。
	Similarity string
}

// ChatSpec 声明 Chat 角色的固定端点与模型（U-1 待定值由调用方给出）。
type ChatSpec struct {
	Endpoint string
	Model    string
	// MaxOutputTokens 是本步输出 token 预留上限（正整数，不得为 0）。
	MaxOutputTokens int
}

// EmbedSpec 声明 Embedding 角色的固定端点、模型与声明维度（P-1 判据的声明值）。
type EmbedSpec struct {
	Endpoint   string
	Model      string
	Dimensions int
	// SupportsDimensionsRequest 是本地声明，不是真实能力证明（U-4 未决前保持 false）。
	SupportsDimensionsRequest bool
}

// RerankSpec 声明 Rerank 角色的完整 URL、模型与 topK（URL 由 BuildRerankRole 直接使用）。
type RerankSpec struct {
	URL   string
	Model string
	TopK  int
}

// Authorization 是在线执行的授权边界。离线骨架不要求授权，但会原样携带与校验。
type Authorization struct {
	OnlineApproved bool
	Scope          string
	ApprovedBy     string
	ApprovedAt     string
}

// Validate 校验授权声明：未批准时不做额外要求；已批准时必须完整声明范围与责任人/时点。
func (a Authorization) Validate() error {
	if !a.OnlineApproved {
		return nil
	}
	switch a.Scope {
	case ScopeS1, ScopeS2, ScopeS3:
	default:
		return fmt.Errorf("%w: authorization.scope must be one of S-1/S-2/S-3, got %q", ErrNotConfigured, a.Scope)
	}
	if strings.TrimSpace(a.ApprovedBy) == "" {
		return fmt.Errorf("%w: authorization.approved_by is required when online execution is approved", ErrNotConfigured)
	}
	if strings.TrimSpace(a.ApprovedAt) == "" {
		return fmt.Errorf("%w: authorization.approved_at is required when online execution is approved", ErrNotConfigured)
	}
	return nil
}

// RetryPolicy 是探针侧的受控重试声明。
//
// 每次重试都是"再一次真实发送"，因此必须重新经过同一预算 owner 的预留→承诺（W-1）；
// MaxAttempts 必须是显式正整数，0 不表示不限。
type RetryPolicy struct {
	MaxAttempts int
	// RetryOnTransportError 仅对传输层错误（连接中断/EOF 等）重试；
	// HTTP 非预期状态、重定向被禁止、校验链拒绝、预留失败与 ctx 取消一律不重试。
	RetryOnTransportError bool
}

// Validate 校验重试声明。
func (r RetryPolicy) Validate() error {
	if r.MaxAttempts <= 0 {
		return fmt.Errorf("%w: retry.max_attempts must be a positive integer", ErrRetryUnset)
	}
	return nil
}

// Plan 是探针的全部固定输入：目标、三角色参数、预算、重试与授权。
// 所有字段均由调用方在**授权文本**中给出；本包不填任何默认数值。
type Plan struct {
	Target        TargetSpec
	Chat          ChatSpec
	Embed         EmbedSpec
	Rerank        RerankSpec
	Budget        BudgetPlan
	Retry         RetryPolicy
	Authorization Authorization
}

// Validate 校验全部固定输入是否已配置且自洽。
// 任一缺失按 ErrNotConfigured 显式失败（不猜测、不填默认值）。
func (p Plan) Validate() error {
	if err := requireText("target.index_name", p.Target.IndexName); err != nil {
		return err
	}
	// 复用生产的物理目标规则（复核 P2）：别名表达式、通配符、多目标、日期数学等
	// 都不是可绑定目标（``bad,*`` 会在 vector.BindPhysicalTarget 处才失败）。
	// 这类配置纯本地可判定，必须在 Readiness 与执行入口同时被拒绝。
	if err := vector.ValidatePhysicalName(p.Target.IndexName); err != nil {
		return fmt.Errorf("%w: target.index_name: %w", ErrNotConfigured, err)
	}
	if err := requireText("target.provider_name", p.Target.ProviderName); err != nil {
		return err
	}
	if err := requireText("target.similarity", p.Target.Similarity); err != nil {
		return err
	}
	if err := requireText("chat.endpoint", p.Chat.Endpoint); err != nil {
		return err
	}
	if err := requireText("chat.model", p.Chat.Model); err != nil {
		return err
	}
	if p.Chat.MaxOutputTokens <= 0 {
		return fmt.Errorf("%w: chat.max_output_tokens must be a positive integer", ErrNotConfigured)
	}
	if err := requireText("embed.endpoint", p.Embed.Endpoint); err != nil {
		return err
	}
	if err := requireText("embed.model", p.Embed.Model); err != nil {
		return err
	}
	if p.Embed.Dimensions <= 0 {
		return fmt.Errorf("%w: embed.dimensions must be a positive integer", ErrNotConfigured)
	}
	if err := requireText("rerank.url", p.Rerank.URL); err != nil {
		return err
	}
	if err := requireText("rerank.model", p.Rerank.Model); err != nil {
		return err
	}
	if p.Rerank.TopK <= 0 {
		return fmt.Errorf("%w: rerank.top_k must be a positive integer", ErrNotConfigured)
	}
	if err := p.Budget.Validate(); err != nil {
		return err
	}
	if err := p.Retry.Validate(); err != nil {
		return err
	}
	// MUTATION（仅证据用）：跳过完整期望指纹比较，复现“dot 目标被报为就绪”的旧行为。
	return p.Authorization.Validate()
}

// validateTargetIdentity 要求声明的目标与生产 embedding 入口**实际比较的完整
// 期望指纹**一致（复核 P2：预检只检查字段非空时，similarity=dot 会被 Readiness
// 报为就绪，执行时才在 common.NewEmbedding 的完整指纹比较处失败）。
//
// 规则来源是生产实现本身：
//   - 期望指纹由 vector.NewExpectedEmbeddingFingerprint 构造（当前格式版本 + cosine）；
//   - 声明的本地目标指纹由同一构造函数族 vector.NewFingerprint 校验（版本/similarity 合法集）。
//
// 两者按 vector.Fingerprint.Equal 全字段比较，因此 provider/model/dims/similarity 任一
// 不一致都会在预检期失败。它只判定**配置自洽性**：真实资源在预检之后是否被替换
// 或变更，不由静态预检保证（那是运行时 guard / 绑定的职责）。
func (p Plan) validateTargetIdentity() error {
	expected, err := vector.NewExpectedEmbeddingFingerprint(p.Embed.Model, p.Embed.Dimensions, p.Target.ProviderName)
	if err != nil {
		return fmt.Errorf("%w: target identity: %w", ErrNotConfigured, err)
	}
	declared, err := vector.NewFingerprint(vector.FingerprintVersion, p.Embed.Model, p.Embed.Dimensions, p.Target.ProviderName, p.Target.Similarity)
	if err != nil {
		return fmt.Errorf("%w: target.similarity: %w", ErrNotConfigured, err)
	}
	if !declared.Equal(expected) {
		return fmt.Errorf("%w: %w: declared target %s does not match the production expectation %s",
			ErrNotConfigured, vector.ErrFingerprintMismatch, declared.Digest(), expected.Digest())
	}
	return nil
}

// endpointRef 是端点校验的名义-取值对（保持确定性遍历顺序）。
type endpointRef struct {
	name string
	url  string
}

// endpoints 返回三个模型端点（用于端点隔离策略校验）。
func (p Plan) endpoints() []endpointRef {
	return []endpointRef{
		{"chat.endpoint", p.Chat.Endpoint},
		{"embed.endpoint", p.Embed.Endpoint},
		{"rerank.url", p.Rerank.URL},
	}
}

// CheckEndpoints 执行端点隔离策略（只读、无网络请求）：
//   - 端点必须是绝对 http/https URL；
//   - 不得内嵌 userinfo；
//   - 回环地址：本机 fake 允许明文 http；
//   - 非回环地址：默认构建直接拒绝（remoteEndpointsAllowed=false），
//     online 构建允许但必须是 HTTPS。
//
// 远端能力只由构建标签决定，**没有**任何调用方参数可以打开它。
// 错误信息只含字段名与主机名：绝不回显 URL 的 userinfo 或查询参数，
// 避免凭据随错误进入日志与证据。
func CheckEndpoints(p Plan) error {
	for _, ref := range p.endpoints() {
		parsed, err := url.Parse(ref.url)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%w: %s must be an absolute URL (scheme://host)", ErrNotConfigured, ref.name)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("%w: %s must use http or https", ErrNotConfigured, ref.name)
		}
		if parsed.User != nil {
			return fmt.Errorf("%w: %s", ErrEndpointUserInfo, ref.name)
		}
		host := parsed.Hostname()
		if isLoopbackHost(host) {
			continue
		}
		if !remoteEndpointsAllowed {
			return fmt.Errorf("%w: %s host %q is not loopback (this build has no remote capability)", ErrRemoteNotAllowed, ref.name, host)
		}
		if parsed.Scheme != "https" {
			return fmt.Errorf("%w: %s host %q must use https", ErrPlaintextRemote, ref.name, host)
		}
	}
	return nil
}

// HasRemoteEndpoints 报告计划中是否存在非回环端点（只读、无网络请求）。
//
// online 构建用它决定构造期是否必须满足 S-1 授权与"凭据只来自环境变量"的边界；
// 解析失败按"需要远端授权"处理（fail closed），正常的明确错误仍由 CheckEndpoints 给出。
func (p Plan) HasRemoteEndpoints() bool {
	for _, ref := range p.endpoints() {
		parsed, err := url.Parse(ref.url)
		if err != nil {
			return true
		}
		host := parsed.Hostname()
		if host == "" || !isLoopbackHost(host) {
			return true
		}
	}
	return false
}

// RemoteEndpointsAllowed 报告当前构建是否具备远端端点能力（默认构建恒为 false）。
// 它只用于证据与断言：能力由构建标签决定，不由调用方参数决定。
func RemoteEndpointsAllowed() bool { return remoteEndpointsAllowed }

// isLoopbackHost 判断主机名是否为回环地址（localhost 或 127.0.0.0/8、::1）。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireText 校验必填字符串（去空白后非空）。
func requireText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrNotConfigured, name)
	}
	return nil
}

// Credentials 只描述凭据的**存在性**来源（环境变量名），不持有也不回显任何值。
// 本轮不提供真实凭据；在线骨架也只读取而不记录值。
type Credentials struct {
	EnvNames []string
}

// 凭据边界（复核 P2：空凭据误报就绪）。Readiness 与所有执行入口共用下面这一组
// 判定，不允许"就绪检查只看变量是否存在、执行时又拒绝空值"的分裂：
//   - 必须**恰好**声明一个变量名（多声明无法判断实际使用的是哪一个，故 fail closed）；
//   - 变量名不能为空白；
//   - 变量必须存在，且值去空白后非空（空值/纯空白等同缺失）；
//   - 任何错误与证据只出现变量名，**绝不出现变量值**。

// declaredNames 返回去空白后的已声明变量名（保持声明顺序）。
func (c Credentials) declaredNames() []string {
	out := make([]string, 0, len(c.EnvNames))
	for _, name := range c.EnvNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Validate 校验凭据边界；错误只报告变量名与违规种类，不回显变量值。
func (c Credentials) Validate() error {
	if len(c.EnvNames) == 0 {
		return fmt.Errorf("%w: no credential environment variable is declared", ErrMissingCredentials)
	}
	for _, name := range c.EnvNames {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: credential environment variable name is blank", ErrMissingCredentials)
		}
	}
	names := c.declaredNames()
	if len(names) != 1 {
		return fmt.Errorf("%w: exactly one credential environment variable must be declared, got %d (%s)",
			ErrMissingCredentials, len(names), strings.Join(names, ", "))
	}
	name := names[0]
	value, ok := os.LookupEnv(name)
	if !ok {
		return fmt.Errorf("%w: %s is not set", ErrMissingCredentials, name)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is set but empty (an empty value is not a credential)", ErrMissingCredentials, name)
	}
	return nil
}

// Resolve 读取唯一声明的凭据值。返回值只交给构造期 adapter，不写日志与证据。
func (c Credentials) Resolve() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return os.Getenv(c.declaredNames()[0]), nil
}

// Presence 返回每个已声明变量名是否可用（存在且值非空白）；值永不返回。
func (c Credentials) Presence() map[string]bool {
	out := make(map[string]bool, len(c.EnvNames))
	for _, name := range c.EnvNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, ok := os.LookupEnv(name)
		out[name] = ok && strings.TrimSpace(value) != ""
	}
	return out
}

// Missing 返回不可用的环境变量名（不存在或值为空白），保持声明顺序并去重。
func (c Credentials) Missing() []string {
	var missing []string
	seen := make(map[string]bool, len(c.EnvNames))
	for _, name := range c.EnvNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			seen[name] = true
			missing = append(missing, name)
		}
	}
	return missing
}

// StepKind 标识探针步骤的角色。
type StepKind string

// 三角色步骤。
const (
	StepChat   StepKind = "chat"
	StepEmbed  StepKind = "embed"
	StepRerank StepKind = "rerank"
)

// Step 描述一步探针的最小请求。输入使用固定占位短文本，不含业务语料与敏感信息。
type Step struct {
	Kind StepKind
	// Prompt 仅 Chat 使用。
	Prompt string
	// Texts 仅 Embed 使用（固定短文本）。
	Texts []string
	// Query/Docs/TopK 仅 Rerank 使用；Docs 为空时使用 ProbeDocuments()。
	Query string
	Docs  []*schema.Document
	TopK  int
}

// validate 校验步骤输入完整性（缺失即报错，不补默认业务语料）。
func (s Step) validate() error {
	switch s.Kind {
	case StepChat:
		if strings.TrimSpace(s.Prompt) == "" {
			return fmt.Errorf("%w: chat prompt is required", ErrEmptyStep)
		}
	case StepEmbed:
		if len(s.Texts) == 0 {
			return fmt.Errorf("%w: embed texts are required", ErrEmptyStep)
		}
		for i, t := range s.Texts {
			if strings.TrimSpace(t) == "" {
				return fmt.Errorf("%w: embed texts[%d] is empty", ErrEmptyStep, i)
			}
		}
	case StepRerank:
		if strings.TrimSpace(s.Query) == "" {
			return fmt.Errorf("%w: rerank query is required", ErrEmptyStep)
		}
		if s.TopK <= 0 {
			return fmt.Errorf("%w: rerank top_k must be a positive integer", ErrEmptyStep)
		}
	default:
		return fmt.Errorf("%w: unknown step kind %q", ErrEmptyStep, s.Kind)
	}
	return nil
}

// ProbeDocuments 返回探针固定占位文档（不含业务语料）。
func ProbeDocuments() []*schema.Document {
	return []*schema.Document{
		{ID: "probe-doc-1", Content: "probe document alpha"},
		{ID: "probe-doc-2", Content: "probe document beta"},
	}
}

// DefaultSteps 返回最小调用矩阵：Chat 单轮 / Embedding 单条 / Rerank 一次。
// 批量 Embedding 与流式 Chat 属同一 owner 下的追加步骤，由授权文本另行列出。
func DefaultSteps() []Step {
	docs := ProbeDocuments()
	return []Step{
		{Kind: StepChat, Prompt: "probe ping"},
		{Kind: StepEmbed, Texts: []string{"probe document"}},
		{Kind: StepRerank, Query: "probe query", Docs: docs, TopK: len(docs)},
	}
}

// StepReport 是单步的离线证据：尝试次数、预算结算计数与响应形状摘要。
// 不记录向量内容、模型输出正文或任何凭据。
type StepReport struct {
	Step    StepKind
	Skipped bool // 因前序停止而未启动（W-5 证据）
	// Attempts 是实际发起的模型调用次数（含受控重试）。
	Attempts int
	// CommittedAttempts 是该步骤请求级 owner 的承诺计数（应等于 Attempts）。
	CommittedAttempts int64
	InputUsed         int64
	OutputUsed        int64
	InFlight          int64
	// Usage 记录 Chat 的 usage 可解析性："known"/"unknown"/"n/a"（unknown 不记 0）。
	Usage string
	// VectorCount/VectorDims 是 Embedding 返回数量与每条向量长度（P-1/P-2 证据）。
	VectorCount int
	VectorDims  []int
	// RerankResults 是 Rerank 返回条数（P-3 证据）。
	RerankResults int
	Duration      time.Duration
}

// Report 是一次探针运行的汇总（含进程级预算终值）。
//
// StopReason 只允许写**脱敏摘要**（见 errcat.go）：固定类别名 + 可能的 HTTP 状态码，
// 绝不写服务端响应正文、URL 查询参数或凭据。原始错误仍由调用方持有的 error 保留。
type Report struct {
	Steps         []StepReport
	ProcessBudget BudgetSnapshotView
	Stopped       bool
	StopReason    string
}

// BudgetSnapshotView 是 providers.BudgetSnapshot 的证据视图（避免调用方再去读组件）。
type BudgetSnapshotView struct {
	AttemptsUsed      int64
	CommittedAttempts int64
	InFlight          int64
	InputReserved     int64
	OutputReserved    int64
	InputUsed         int64
	OutputUsed        int64
}
