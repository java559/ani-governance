package data

import "time"

// PlatformComponentLogEntry 是一条归一化后的组件日志（对外六字段 + 去重所需的
// 索引/文档 ID）。Skipped 由读取层单独统计，不进入该结构。
type PlatformComponentLogEntry struct {
	Container string
	Level     string
	Message   string
	Pod       string
	Stream    string
	Timestamp time.Time
	// Index 与 ID 只用于连接内去重，不对外暴露。
	Index string
	ID    string
}

// PlatformComponentLogBatch 是一轮查询的结果。
type PlatformComponentLogBatch struct {
	Entries []PlatformComponentLogEntry
	// RequestedSize 是本轮实际下发的 size（可能因响应超限被降级），
	// 调用方据此判断"是否取满"，不能用固定常量。
	RequestedSize int
	// Skipped 是因缺 message 或缺 @timestamp 被跳过的条数。
	Skipped int
}

// PlatformComponentLogWindow 只承载一条 SSE 连接内的轮次状态：
// 已发送的 (_index,_id) 去重集合，以及该集合的大小上限。
//
// 刻意没有水位、没有 search_after、没有跨连接状态、没有 Redis 游标：
// 一期是 8 秒短连接，跨连接 exactly-once 不在范围内（实施方案 §6.2）。
type PlatformComponentLogWindow struct {
	seen map[string]struct{}
	max  int
}

// NewPlatformComponentLogWindow 创建一个去重窗口。max 为集合上限，
// 达到上限表示本次连接已无法保证不重复，调用方应显式结束而不是继续发。
func NewPlatformComponentLogWindow(max int) *PlatformComponentLogWindow {
	if max <= 0 {
		max = 10000
	}
	return &PlatformComponentLogWindow{seen: make(map[string]struct{}, max), max: max}
}

// Seen 报告该文档是否已在本连接发送过。
func (w *PlatformComponentLogWindow) Seen(index, id string) bool {
	if w == nil || id == "" {
		return false
	}
	_, ok := w.seen[index+"\x00"+id]
	return ok
}

// Mark 记录已发送的文档。
func (w *PlatformComponentLogWindow) Mark(index, id string) {
	if w == nil || id == "" {
		return
	}
	w.seen[index+"\x00"+id] = struct{}{}
}

// Full 报告去重集合是否已达上限。
func (w *PlatformComponentLogWindow) Full() bool {
	return w != nil && len(w.seen) >= w.max
}

// Len 返回已记录的文档数（测试与观测用）。
func (w *PlatformComponentLogWindow) Len() int {
	if w == nil {
		return 0
	}
	return len(w.seen)
}
