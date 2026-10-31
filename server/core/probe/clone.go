package probe

import "github.com/cloudwego/eino/schema"

// 本文件提供构造入口与运行入口的**防御性深拷贝**（复核 P2：浅拷贝可变 Plan
// 导致真实数据竞争）。
//
// 责任划分（对调用方的明确契约）：
//   - 构造期间：调用方不得与 NewChain 并发修改传入的 Plan / 目标；
//     NewChain 会读取这些值做校验与构造。
//   - 构造返回后：Chain 持有的是 Plan 的深拷贝，调用方随后修改原 Plan
//     （含 Budget.Unlimited 切片、授权字段）既不改变已固定的预算/授权，
//     也不会与探针运行期的读取竞争。
//   - 调用期间：Run / RunStep 在入口处克隆本次传入的步骤（Texts 与
//     Documents 含 MetaData），因此调用返回后再修改原切片不影响已完成的运行。
//     "在调用进行中并发修改入参"仍是调用方违约，入口只读取一次。

// Clone 返回 Plan 的防御性深拷贝。Plan 内唯一的切片引用是 Budget.Unlimited，
// 其余字段都是值类型（字符串/整数/时长/布尔）。
func (p Plan) Clone() Plan {
	out := p
	if p.Budget.Unlimited != nil {
		out.Budget.Unlimited = append([]string(nil), p.Budget.Unlimited...)
	}
	return out
}

// clone 返回步骤的防御性深拷贝：输入文本与文档（含 MetaData）都独立复制，
// 避免调用方在运行期修改自己的切片影响已准入的发送。
func (s Step) clone() Step {
	out := s
	if s.Texts != nil {
		out.Texts = append([]string(nil), s.Texts...)
	}
	out.Docs = cloneDocuments(s.Docs)
	return out
}

// cloneSteps 深拷贝整个步骤矩阵（Run 的入口使用）。
func cloneSteps(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	out := make([]Step, len(steps))
	for i, step := range steps {
		out[i] = step.clone()
	}
	return out
}

// cloneDocuments 深拷贝文档切片与每个文档的 MetaData map。
func cloneDocuments(docs []*schema.Document) []*schema.Document {
	if docs == nil {
		return nil
	}
	out := make([]*schema.Document, len(docs))
	for i, doc := range docs {
		if doc == nil {
			continue
		}
		copied := *doc
		if doc.MetaData != nil {
			meta := make(map[string]any, len(doc.MetaData))
			for k, v := range doc.MetaData {
				meta[k] = v
			}
			copied.MetaData = meta
		}
		out[i] = &copied
	}
	return out
}
