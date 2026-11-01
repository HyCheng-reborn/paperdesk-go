package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 已知 Provider 名称与协议（stage2-plan §3.3.1）。
// 2A 仅定义常量并在 Profile 层做非空/归一化校验；
// “ProviderName 不在已知列表即启动报错”的白名单强制在 2B 工厂层实施。
const (
	ProviderBailian = "bailian"
	ProviderOpenAI  = "openai"

	ProtocolOpenAICompat = "openai-compatible"
	ProtocolDashScopeNtv = "dashscope-native"
)

// KeyOptions 约定键名（stage2-plan §3.3.1）。
const (
	KeyOptionStream           = "stream"
	KeyOptionStructuredOutput = "structuredOutput"
	KeyOptionToolCalling      = "toolCalling"
)

// 校验错误（配置缺失/非法在构造期显式返回，不延迟到首次模型调用，见 §3.5）。
var (
	ErrProfileEmptyProvider = errors.New("providers: ProviderName must not be empty")
	ErrProfileEmptyProtocol = errors.New("providers: Protocol must not be empty")
	ErrProfileEmptyModel    = errors.New("providers: Model must not be empty")
	ErrProfileEmptyEndpoint = errors.New("providers: Endpoint must not be empty")
	ErrProfileBadDimensions = errors.New("providers: Dimensions must be >= 0")
	// ErrProfileAmbiguousModel Model/Version 叠加了两种版本表示（如 Model 已含 @ 且 Version 非空）。
	ErrProfileAmbiguousModel = errors.New("providers: Model/Version representation is ambiguous")
	// ErrModelIDInvalid 模型身份 "model" / "model@version" 表示非法（空模型、空版本、多个 @）。
	ErrModelIDInvalid = errors.New("providers: invalid model identity")
)

// NormalizeProviderName 是 Provider 名称的唯一规范化语义：去首尾空白并转小写。
// fingerprint（NewFingerprint）与 Registry Profile（NewProviderProfile）都使用该语义，
// 身份比较必须复用同一函数，不能直接比较配置原文。
func NormalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// SplitModelID 将 "model" / "model@version" 解析为无歧义的两段。
// 拒绝空模型、空版本（"m@"）、多个 @ 以及带空白的空段，避免 "m@" 与 "m" 互为别名。
func SplitModelID(modelID string) (model, version string, err error) {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		return "", "", fmt.Errorf("%w: empty", ErrModelIDInvalid)
	}
	i := strings.Index(trimmed, "@")
	if i < 0 {
		return trimmed, "", nil
	}
	if strings.Index(trimmed[i+1:], "@") >= 0 {
		return "", "", fmt.Errorf("%w: multiple '@' in %q", ErrModelIDInvalid, modelID)
	}
	model = strings.TrimSpace(trimmed[:i])
	version = strings.TrimSpace(trimmed[i+1:])
	if model == "" {
		return "", "", fmt.Errorf("%w: empty model in %q", ErrModelIDInvalid, modelID)
	}
	if version == "" {
		return "", "", fmt.Errorf("%w: empty version in %q", ErrModelIDInvalid, modelID)
	}
	return model, version, nil
}

// CanonicalModelID 返回无歧义规范化后的模型身份（Model 或 Model@Version）。
// fingerprint、身份比较与 Registry Profile 使用同一表示，空版本等非法输入直接报错。
func CanonicalModelID(modelID string) (string, error) {
	model, version, err := SplitModelID(modelID)
	if err != nil {
		return "", err
	}
	if version == "" {
		return model, nil
	}
	return model + "@" + version, nil
}

// ErrModelVersionNotExpressible 表示协议无法在请求中表达模型版本。
//
// 当前两种协议（openai-compatible / dashscope-native）的 wire model 字段只有
// 模型名，没有独立的版本身份参数。若身份含 "@version" 而请求只发送模型名，
// fingerprint / 角色标签会声明一个实际未生效的版本，因此必须 fail closed，
// 不能只把版本写进身份标签。
var ErrModelVersionNotExpressible = errors.New("providers: protocol cannot express model version in request")

// RequestModelID 返回实际随请求发送的 model 值，是“身份 == 请求模型”不变式的
// 唯一实现：无版本身份返回模型名；带版本身份返回 ErrModelVersionNotExpressible。
// 非法身份（空模型、空版本、多个 @）保持 ErrModelIDInvalid。
func RequestModelID(modelID string) (string, error) {
	model, version, err := SplitModelID(modelID)
	if err != nil {
		return "", err
	}
	if version != "" {
		return "", fmt.Errorf("%w: model=%q version=%q", ErrModelVersionNotExpressible, model, version)
	}
	return model, nil
}

// RequestModel 返回该 Profile 实际随请求发送的 model 值。
// 与 RequestModelID 共享同一实现，因此所有角色构造器对版本身份的处理一致。
func (p *ProviderProfile) RequestModel() (string, error) {
	if p == nil {
		return "", ErrNilProfile
	}
	return RequestModelID(p.ModelID())
}

// ProviderProfile 是 Registry 构造时接收的不可变 Provider 身份描述。
// 一旦通过 NewProviderProfile 构造完成即视为只读；需要修改时应 Clone 后重新构造。
//
// 字段含义见 stage2-plan §3.3.1：CredentialIdentity 仅存 API Key 的 hash 或 ID，
// 不存明文；Dimensions 为 Embedding 角色专用。
type ProviderProfile struct {
	ProviderName       string            // bailian / openai
	Protocol           string            // openai-compatible / dashscope-native
	Endpoint           string            // 如 dashscope.aliyuncs.com/compatible-mode/v1
	Region             string            // 如 cn-beijing
	Model              string            // 如 qwen-plus / text-embedding-v4
	Version            string            // 模型版本（可选）
	Dimensions         int               // Embedding 角色专用（如 1024）
	KeyOptions         map[string]string // stream / structuredOutput / toolCalling
	CredentialIdentity string            // API Key 的 hash 或 ID，不存明文
	Capabilities       Capabilities
}

// NewProviderProfile 校验并归一化输入，返回不可变 Profile 指针。
// 关键保证：
//   - 对入参 KeyOptions 做防御性深拷贝，调用方后续修改入参 map 不影响已构造 Profile；
//   - 字符串字段 TrimSpace，ProviderName/Protocol 归一化为小写（枚举式取值）；
//   - Model/Version/Endpoint/Region/CredentialIdentity 保持大小写敏感（模型 ID 区分大小写）。
//
// 任一必填项缺失或 Dimensions < 0 返回明确错误，不静默回退。
func NewProviderProfile(in ProviderProfile) (*ProviderProfile, error) {
	p := ProviderProfile{
		ProviderName:       NormalizeProviderName(in.ProviderName),
		Protocol:           strings.ToLower(strings.TrimSpace(in.Protocol)),
		Endpoint:           strings.TrimSpace(in.Endpoint),
		Region:             strings.TrimSpace(in.Region),
		Model:              strings.TrimSpace(in.Model),
		Version:            strings.TrimSpace(in.Version),
		Dimensions:         in.Dimensions,
		CredentialIdentity: strings.TrimSpace(in.CredentialIdentity),
		Capabilities:       in.Capabilities, // 纯值类型，直接复制
	}
	// 无歧义模型身份："model@version" 只允许出现在 Model 字段或 Model+Version 二者之一，
	// 不允许两种表示叠加。空版本（"m@"）在 validate 中经 SplitModelID 拒绝。
	if strings.Contains(p.Model, "@") {
		if p.Version != "" {
			return nil, fmt.Errorf("%w: Model=%q Version=%q", ErrProfileAmbiguousModel, p.Model, p.Version)
		}
		model, version, splitErr := SplitModelID(p.Model)
		if splitErr != nil {
			return nil, splitErr
		}
		p.Model, p.Version = model, version
	} else if p.Version != "" {
		if _, _, splitErr := SplitModelID(p.Model + "@" + p.Version); splitErr != nil {
			return nil, splitErr
		}
	}
	// 防御性复制 KeyOptions（同时归一化键：TrimSpace）
	if in.KeyOptions != nil {
		p.KeyOptions = make(map[string]string, len(in.KeyOptions))
		for k, v := range in.KeyOptions {
			p.KeyOptions[strings.TrimSpace(k)] = v
		}
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// validate 校验必填项与取值范围。
func (p *ProviderProfile) validate() error {
	if p.ProviderName == "" {
		return ErrProfileEmptyProvider
	}
	if p.Protocol == "" {
		return ErrProfileEmptyProtocol
	}
	if p.Model == "" {
		return ErrProfileEmptyModel
	}
	if _, _, err := SplitModelID(p.ModelID()); err != nil {
		return err
	}
	if p.Endpoint == "" {
		return ErrProfileEmptyEndpoint
	}
	if p.Dimensions < 0 {
		return fmt.Errorf("%w: got %d", ErrProfileBadDimensions, p.Dimensions)
	}
	return nil
}

// Clone 返回 Profile 的深拷贝（KeyOptions 独立复制），用于安全派生新 Profile。
func (p *ProviderProfile) Clone() *ProviderProfile {
	if p == nil {
		return nil
	}
	cp := *p // 值拷贝（Capabilities 为纯值）
	if p.KeyOptions != nil {
		cp.KeyOptions = make(map[string]string, len(p.KeyOptions))
		for k, v := range p.KeyOptions {
			cp.KeyOptions[k] = v
		}
	}
	return &cp
}

// snapshot 返回值类型深拷贝（KeyOptions 独立复制），供 adapter.Profile() 暴露
// 不可变身份而不泄漏可写内部引用（stage2 复核 B3）。nil 安全返回零值。
func (p *ProviderProfile) snapshot() ProviderProfile {
	if p == nil {
		return ProviderProfile{}
	}
	cp := *p
	if p.KeyOptions != nil {
		cp.KeyOptions = make(map[string]string, len(p.KeyOptions))
		for k, v := range p.KeyOptions {
			cp.KeyOptions[k] = v
		}
	}
	return cp
}

// KeyOption 安全读取单个 KeyOptions 值（不暴露内部 map 引用）。
func (p *ProviderProfile) KeyOption(key string) (string, bool) {
	if p == nil || p.KeyOptions == nil {
		return "", false
	}
	v, ok := p.KeyOptions[strings.TrimSpace(key)]
	return v, ok
}

// ModelID 返回模型身份标识，供 EmbedRole.ModelID 与 2C 索引 fingerprint 使用。
// 同维度不等于同模型（§10.2），因此身份必须包含 Model 与 Version。
func (p *ProviderProfile) ModelID() string {
	if p == nil {
		return ""
	}
	if p.Version == "" {
		return p.Model
	}
	return p.Model + "@" + p.Version
}

// CacheKey 返回稳定归一化、无歧义的缓存键（stage2 复核 B3）。
//
// 编码方式：每个字段以「长度前缀 | 名称 = 长度前缀 | 值 ;」写入，避免未转义拼接造成的
// 确定碰撞（如 KeyOptions {"a":"1,b=2"} 与 {"a":"1","b":"2"} 曾拼出同一串）；
// KeyOptions 按键排序后逐个长度前缀编码，保证插入顺序无关。最终对无歧义序列取 SHA-256。
//
// 保证：
//   - 仅大小写（ProviderName/Protocol）或首尾空白不同的 Profile 得到相同键；
//   - KeyOptions 插入顺序不同但内容相同的 Profile 得到相同键；
//   - Model/Version/Dimensions/Endpoint/Region/CredentialIdentity/Capabilities/KeyOptions
//     任一不同则键不同（无确定碰撞）。
func (p *ProviderProfile) CacheKey() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	writeCacheField(&b, "provider", p.ProviderName) // 已在构造期小写归一
	writeCacheField(&b, "protocol", p.Protocol)     // 已在构造期小写归一
	writeCacheField(&b, "endpoint", p.Endpoint)
	writeCacheField(&b, "region", p.Region)
	writeCacheField(&b, "model", p.Model)
	writeCacheField(&b, "version", p.Version)
	writeCacheField(&b, "dims", strconv.Itoa(p.Dimensions))
	writeCacheField(&b, "cred", p.CredentialIdentity)
	// Capabilities 参与身份：能力不同的 Profile 不可共享缓存实例
	// 2C-3: SupportsDimensionsRequest 加入身份指纹
	writeCacheField(&b, "cap", fmt.Sprintf("%t,%t,%t,%t,%t",
		p.Capabilities.Stream, p.Capabilities.StructuredOutput,
		p.Capabilities.ToolCalling, p.Capabilities.Usage,
		p.Capabilities.SupportsDimensionsRequest))
	// KeyOptions 排序后逐个长度前缀编码，保证顺序无关且无歧义
	if len(p.KeyOptions) > 0 {
		keys := make([]string, 0, len(p.KeyOptions))
		for k := range p.KeyOptions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeCacheField(&b, "opt:"+k, p.KeyOptions[k])
		}
	}
	return sha256Hex(b.String())
}

// writeCacheField 以「len(name)|name=len(value)|value;」无歧义写入一个字段。
// 长度前缀使任何内嵌分隔符/等号的值都不会与另一组字段拼出相同串。
func writeCacheField(b *strings.Builder, name, value string) {
	b.WriteString(strconv.Itoa(len(name)))
	b.WriteByte('|')
	b.WriteString(name)
	b.WriteByte('=')
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte('|')
	b.WriteString(value)
	b.WriteByte(';')
}

// sha256Hex 返回输入的 SHA-256 十六进制摘要（基于无歧义序列，避免超长键）。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
