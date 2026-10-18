package providers

import (
	"errors"
	"testing"
)

// TestProfile_KeyOptionsCopy：构造期对 KeyOptions 做防御性深拷贝，
// 调用方后续修改入参 map 不影响已构造 Profile；Clone 亦独立复制。
func TestProfile_KeyOptionsCopy(t *testing.T) {
	opts := map[string]string{
		KeyOptionStream:           "true",
		KeyOptionStructuredOutput: "false",
	}
	p, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.com/v1",
		Model:        "qwen-plus",
		KeyOptions:   opts,
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}

	// 构造完成后修改入参 map：不得影响已构造 Profile。
	opts[KeyOptionStream] = "mutated"
	opts["extra"] = "injected"
	delete(opts, KeyOptionStructuredOutput)

	if v, ok := p.KeyOption(KeyOptionStream); !ok || v != "true" {
		t.Fatalf("KeyOptions must be defensively copied: stream=%q ok=%v", v, ok)
	}
	if v, ok := p.KeyOption(KeyOptionStructuredOutput); !ok || v != "false" {
		t.Fatalf("structuredOutput lost after external mutation: %q ok=%v", v, ok)
	}
	if _, ok := p.KeyOption("extra"); ok {
		t.Fatal("externally injected key must not appear in Profile")
	}

	// Clone 必须独立复制 KeyOptions：修改克隆不影响原 Profile。
	c := p.Clone()
	if c == p {
		t.Fatal("Clone must return a distinct pointer")
	}
	c.KeyOptions[KeyOptionStream] = "cloned"
	c.KeyOptions["cloneOnly"] = "x"
	if v, _ := p.KeyOption(KeyOptionStream); v != "true" {
		t.Fatalf("mutating clone leaked into original: stream=%q", v)
	}
	if _, ok := p.KeyOption("cloneOnly"); ok {
		t.Fatal("clone-only key leaked into original Profile")
	}

	// nil KeyOptions：不分配 map，KeyOption 安全返回 false。
	pn, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.com/v1",
		Model:        "qwen-plus",
	})
	if err != nil {
		t.Fatalf("NewProviderProfile(nil opts): %v", err)
	}
	if pn.KeyOptions != nil {
		t.Fatalf("nil KeyOptions must stay nil, got %v", pn.KeyOptions)
	}
	if _, ok := pn.KeyOption(KeyOptionStream); ok {
		t.Fatal("KeyOption on nil map must return false")
	}
}

// TestProfile_CacheKeyNormalization：缓存键稳定归一化——
// 仅大小写/首尾空白/KeyOptions 插入顺序不同的 Profile 得到相同键；
// Model/Version/Dimensions/Endpoint/Region/CredentialIdentity/Capabilities/KeyOptions 值
// 不同则键不同；同 Profile 多次计算稳定。
func TestProfile_CacheKeyNormalization(t *testing.T) {
	base := ProviderProfile{
		ProviderName:       ProviderBailian,
		Protocol:           ProtocolOpenAICompat,
		Endpoint:           "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Region:             "cn-beijing",
		Model:              "qwen-plus",
		Version:            "1",
		Dimensions:         0,
		CredentialIdentity: "cred-hash-abc",
		Capabilities:       Capabilities{Stream: true, Usage: true},
		KeyOptions:         map[string]string{"a": "1", "b": "2"},
	}

	mustBuild := func(in ProviderProfile) *ProviderProfile {
		t.Helper()
		pp, err := NewProviderProfile(in)
		if err != nil {
			t.Fatalf("NewProviderProfile: %v", err)
		}
		return pp
	}
	// freshBase 返回值副本且 KeyOptions 独立，避免各变异互相污染。
	freshBase := func() ProviderProfile {
		in := base
		in.KeyOptions = map[string]string{"a": "1", "b": "2"}
		return in
	}

	p1 := mustBuild(freshBase())

	// 稳定：同 Profile 多次计算一致。
	if p1.CacheKey() != p1.CacheKey() {
		t.Fatal("CacheKey must be stable across calls")
	}

	// 大小写 + 首尾空白差异（ProviderName/Protocol 归一小写，字符串字段 TrimSpace）→ 相同键。
	vCase := freshBase()
	vCase.ProviderName = "  BAILIAN  "
	vCase.Protocol = "OpenAI-Compatible"
	vCase.Model = " qwen-plus "
	vCase.Endpoint = "  https://dashscope.aliyuncs.com/compatible-mode/v1  "
	vCase.Region = " cn-beijing "
	if got, want := mustBuild(vCase).CacheKey(), p1.CacheKey(); got != want {
		t.Fatalf("case/whitespace variant must share key:\n got=%q\nwant=%q", got, want)
	}

	// KeyOptions 插入顺序不同、内容相同 → 相同键。
	vOrder := freshBase()
	vOrder.KeyOptions = map[string]string{"b": "2", "a": "1"}
	if got, want := mustBuild(vOrder).CacheKey(), p1.CacheKey(); got != want {
		t.Fatalf("KeyOptions insertion order must not change key:\n got=%q\nwant=%q", got, want)
	}

	// 差异字段 → 不同键。
	mutations := []struct {
		name   string
		mutate func(*ProviderProfile)
	}{
		{"provider", func(pp *ProviderProfile) { pp.ProviderName = ProviderOpenAI }},
		{"protocol", func(pp *ProviderProfile) { pp.Protocol = ProtocolDashScopeNtv }},
		{"endpoint", func(pp *ProviderProfile) { pp.Endpoint = "https://other.example.com/v1" }},
		{"region", func(pp *ProviderProfile) { pp.Region = "cn-hangzhou" }},
		{"model", func(pp *ProviderProfile) { pp.Model = "qwen-max" }},
		{"version", func(pp *ProviderProfile) { pp.Version = "2" }},
		{"dimensions", func(pp *ProviderProfile) { pp.Dimensions = 1024 }},
		{"credential", func(pp *ProviderProfile) { pp.CredentialIdentity = "cred-hash-zzz" }},
		{"capability", func(pp *ProviderProfile) { pp.Capabilities.ToolCalling = true }},
		{"keyopt-value", func(pp *ProviderProfile) { pp.KeyOptions["a"] = "9" }},
		{"keyopt-key", func(pp *ProviderProfile) {
			delete(pp.KeyOptions, "b")
			pp.KeyOptions["c"] = "3"
		}},
	}
	for _, m := range mutations {
		in := freshBase()
		m.mutate(&in)
		got := mustBuild(in).CacheKey()
		if got == p1.CacheKey() {
			t.Fatalf("mutation %q must produce a different cache key", m.name)
		}
	}
}

// TestProfile_Validation：必填项缺失或非法 Dimensions 在构造期显式报错，不静默回退。
func TestProfile_Validation(t *testing.T) {
	valid := ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.com/v1",
		Model:        "qwen-plus",
	}
	if _, err := NewProviderProfile(valid); err != nil {
		t.Fatalf("valid profile must construct: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*ProviderProfile)
		wantErr error
	}{
		{"empty-provider", func(p *ProviderProfile) { p.ProviderName = "  " }, ErrProfileEmptyProvider},
		{"empty-protocol", func(p *ProviderProfile) { p.Protocol = "" }, ErrProfileEmptyProtocol},
		{"empty-model", func(p *ProviderProfile) { p.Model = "" }, ErrProfileEmptyModel},
		{"empty-endpoint", func(p *ProviderProfile) { p.Endpoint = "" }, ErrProfileEmptyEndpoint},
		{"bad-dimensions", func(p *ProviderProfile) { p.Dimensions = -1 }, ErrProfileBadDimensions},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			in := valid
			c.mutate(&in)
			_, err := NewProviderProfile(in)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

// TestProfile_ModelID：Version 为空时返回 Model，否则返回 Model@Version。
func TestProfile_ModelID(t *testing.T) {
	p, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.com/v1",
		Model:        "text-embedding-v4",
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	if got := p.ModelID(); got != "text-embedding-v4" {
		t.Fatalf("ModelID without version = %q", got)
	}

	pv, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.com/v1",
		Model:        "text-embedding-v4",
		Version:      "2",
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	if got := pv.ModelID(); got != "text-embedding-v4@2" {
		t.Fatalf("ModelID with version = %q", got)
	}

	// nil Profile 安全返回空串。
	var np *ProviderProfile
	if got := np.ModelID(); got != "" {
		t.Fatalf("nil ModelID = %q, want empty", got)
	}
	if got := np.CacheKey(); got != "" {
		t.Fatalf("nil CacheKey = %q, want empty", got)
	}
	if np.Clone() != nil {
		t.Fatal("nil Clone must return nil")
	}
}
