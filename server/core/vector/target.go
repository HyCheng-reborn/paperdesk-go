package vector

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/elastic/go-elasticsearch/v8"
)

var (
	ErrPhysicalTargetRequired   = errors.New("vector: a single concrete physical target is required")
	ErrResourceBindingNotReady  = errors.New("vector: physical resource binding is not ready")
	ErrManagedLifecycleRequired = errors.New("vector: managed resource lifecycle must be explicitly confirmed")
)

var physicalName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)

func CheckManagedLifecycle(cfg *Config) error {
	if cfg == nil {
		return ErrManagedLifecycleRequired
	}
	if err := ValidatePhysicalName(cfg.IndexName); err != nil {
		return err
	}
	if cfg.Type == "qdrant" {
		return fmt.Errorf("%w: Qdrant incarnation/namespace is unresolved", ErrResourceBindingNotReady)
	}
	if !cfg.ManagedResourceLifecycle {
		return ErrManagedLifecycleRequired
	}
	return nil
}

// ValidatePhysicalName excludes aliases' resolution expressions, wildcards,
// date math, remote cluster selectors and multiple targets. Backend responses
// must additionally prove that the name is concrete, rather than an alias.
func ValidatePhysicalName(name string) error {
	if !physicalName.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrPhysicalTargetRequired, name)
	}
	return nil
}

// TargetBinding 是 BindPhysicalTarget 产出的、不可伪造的物理目标绑定。
//
// P1-3: 仅凭“非 nil 回调 + 生命周期布尔 + 非空名称”不能证明已绑定。
// 本类型字段全部私有，只能由 BindPhysicalTarget 在读取并校验具体 ES 目标
// （名称、索引 UUID、fingerprint）后生成；零值或手工值必然校验失败。
// 它不提供后端 CAS：部署仍需保证名称不复用、资源不被替换。
type TargetBinding struct {
	target      string
	resourceID  string
	fingerprint Fingerprint
	store       VectorStore
	esClient    *elasticsearch.Client
}

// Target 返回已校验的具体物理名称。
func (b *TargetBinding) Target() string {
	if b == nil {
		return ""
	}
	return b.target
}

// ResourceID 返回绑定时读到的具体资源 ID（ES index UUID）。
func (b *TargetBinding) ResourceID() string {
	if b == nil {
		return ""
	}
	return b.resourceID
}

// Fingerprint 返回绑定时校验通过的 fingerprint 值副本。
func (b *TargetBinding) Fingerprint() Fingerprint {
	if b == nil {
		return Fingerprint{}
	}
	return b.fingerprint
}

// Check 只做无 I/O 的自洽与归属校验：内部字段完整、名称一致，
// 调用方提供了 ES client 时必须与绑定捕获的 client 相同。
func (b *TargetBinding) Check(target string, client *elasticsearch.Client) error {
	if b == nil || b.store == nil || b.esClient == nil {
		return fmt.Errorf("%w: target binding is not initialized", ErrResourceBindingNotReady)
	}
	if b.target == "" || b.resourceID == "" {
		return fmt.Errorf("%w: target binding lacks name or resource ID", ErrResourceBindingNotReady)
	}
	if err := ValidatePhysicalName(b.target); err != nil {
		return err
	}
	if !b.fingerprint.Valid() {
		return fmt.Errorf("%w: target binding has invalid fingerprint", ErrFingerprintCorrupted)
	}
	if target != b.target {
		return fmt.Errorf("%w: binding target %q != configured %q", ErrPhysicalTargetRequired, b.target, target)
	}
	if client != nil && client != b.esClient {
		return fmt.Errorf("%w: binding belongs to a different backend client", ErrResourceBindingNotReady)
	}
	return nil
}

// Verify 重新读取绑定的物理目标，确认资源 ID 与 fingerprint 未变。
// 它是运行时（embedding 前后）与构建期的可验证绑定入口。
func (b *TargetBinding) Verify(ctx context.Context) error {
	if err := b.Check(b.Target(), nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, currentID, err := b.store.ReadFingerprint(ctx, b.target)
	if err != nil {
		return fmt.Errorf("%w: read bound target %q: %w", ErrResourceBindingNotReady, b.target, err)
	}
	if currentID == "" {
		return fmt.Errorf("%w: bound target %q has empty resource ID", ErrFingerprintCorrupted, b.target)
	}
	if currentID != b.resourceID {
		return fmt.Errorf("%w: target %q", ErrResourceReplaced, b.target)
	}
	want := b.fingerprint
	return VerifyFingerprint(stored, &want)
}

// Guard 返回绑定对应的运行时校验函数（embedding 前后调用）。
// 与旧版函数的区别：闭包持有已校验的绑定，不再依赖调用方传入任意回调。
func (b *TargetBinding) Guard() func(context.Context) error {
	if b == nil {
		return nil
	}
	return func(ctx context.Context) error { return b.Verify(ctx) }
}

// BindPhysicalTarget 读取并校验唯一具体 ES 物理目标（名称、资源 ID、fingerprint），
// 返回不可伪造的 TargetBinding。
// 不提供后端 CAS：部署必须禁止名称复用、资源替换、后端切换与自动建索引。
func BindPhysicalTarget(ctx context.Context, vs VectorStore, name string, expected *Fingerprint) (*TargetBinding, error) {
	if vs == nil || expected == nil {
		return nil, ErrResourceBindingNotReady
	}
	if err := ValidatePhysicalName(name); err != nil {
		return nil, err
	}
	if _, qdrant := vs.(*QdrantVectorStore); qdrant {
		return nil, fmt.Errorf("%w: Qdrant incarnation/namespace is unresolved", ErrResourceBindingNotReady)
	}
	// P1-3: 绑定的具体目标只能是已批准的 ES 实现；其它实现无法证明物理绑定。
	es, ok := vs.(*ESVectorStore)
	if !ok {
		return nil, fmt.Errorf("%w: only the concrete ES target can be bound", ErrResourceBindingNotReady)
	}
	stored, id, err := vs.ReadFingerprint(ctx, name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: empty resource ID", ErrFingerprintCorrupted)
	}
	if err := VerifyFingerprint(stored, expected); err != nil {
		return nil, err
	}
	// 捕获值，而不是调用方持有的可变 fingerprint 指针。
	binding := &TargetBinding{
		target:      name,
		resourceID:  id,
		fingerprint: *expected,
		store:       vs,
		esClient:    es.GetClient(),
	}
	if err := binding.Check(name, nil); err != nil {
		return nil, err
	}
	return binding, nil
}
