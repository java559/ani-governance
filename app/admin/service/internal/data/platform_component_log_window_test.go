package data

import "testing"

// TestPlatformComponentLogWindowDeduplicatesByIndexAndID 验证去重键是
// (_index,_id) 组合：同 ID 不同索引不算重复（跨日索引轮转时必须各自保留）。
func TestPlatformComponentLogWindowDeduplicatesByIndexAndID(t *testing.T) {
	window := NewPlatformComponentLogWindow(10)
	if window.Seen("ani-logs-2026.10.28", "a1") {
		t.Fatal("fresh window must not report seen")
	}
	window.Mark("ani-logs-2026.10.28", "a1")
	if !window.Seen("ani-logs-2026.10.28", "a1") {
		t.Fatal("marked document must report seen")
	}
	if window.Seen("ani-logs-2026.10.27", "a1") {
		t.Fatal("same id in another index must stay distinct")
	}
	if window.Len() != 1 {
		t.Fatalf("len = %d", window.Len())
	}
}

// TestPlatformComponentLogWindowIgnoresEmptyID 验证缺少 _id 的命中不参与去重：
// 未标记的记录不会被误判成"已发送"。
func TestPlatformComponentLogWindowIgnoresEmptyID(t *testing.T) {
	window := NewPlatformComponentLogWindow(10)
	window.Mark("i", "")
	if window.Seen("i", "") {
		t.Fatal("empty id must never be treated as seen")
	}
	if window.Len() != 0 {
		t.Fatalf("len = %d", window.Len())
	}
}

// TestPlatformComponentLogWindowFull 验证上限触发后可被调用方观察到，
// 由调用方显式结束连接而不是继续发。
func TestPlatformComponentLogWindowFull(t *testing.T) {
	window := NewPlatformComponentLogWindow(2)
	if window.Full() {
		t.Fatal("empty window must not be full")
	}
	window.Mark("i", "1")
	window.Mark("i", "2")
	if !window.Full() {
		t.Fatal("window must report full at cap")
	}
}

// TestPlatformComponentLogWindowNilSafety 验证零值窗口不会 panic：
// service 层在异常路径可能拿到 nil，必须退化为"不去重"而不是崩溃。
func TestPlatformComponentLogWindowNilSafety(t *testing.T) {
	var window *PlatformComponentLogWindow
	if window.Seen("i", "1") || window.Full() || window.Len() != 0 {
		t.Fatal("nil window must be inert")
	}
	window.Mark("i", "1")
}

// TestPlatformComponentLogWindowDefaultCap 验证非正上限回退到确定默认值，
// 不允许退化成无限集合。
func TestPlatformComponentLogWindowDefaultCap(t *testing.T) {
	window := NewPlatformComponentLogWindow(0)
	if window.max != 10000 {
		t.Fatalf("default cap = %d", window.max)
	}
}
