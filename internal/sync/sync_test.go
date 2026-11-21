package sync

import "testing"

func TestPushRecordDedupKey(t *testing.T) {
	r := PushRecord{UserID: "subuser:su-1", PeriodID: "2026-11", Seq: 3, Up: 100, Down: 200}
	if r.DedupKey() != "subuser:su-1/2026-11/3" {
		t.Fatalf("去重键不对：%s", r.DedupKey())
	}
	// Seq 不同就是另一条推送，不能判重。
	r2 := r
	r2.Seq = 4
	if r.DedupKey() == r2.DedupKey() {
		t.Fatal("Seq 不同时去重键必须不同")
	}
}

func TestUserRowValidate(t *testing.T) {
	ok := UserRow{Key: "subuser:su-1", Email: "u-sub-1@node", NodeUUID: "11111111-2222-3333-4444-555555555555", LimitBytes: 1073741824}
	if err := ok.Validate(); err != nil {
		t.Fatalf("完整的用户行应通过校验：%v", err)
	}
	for _, bad := range []UserRow{
		{Email: "u-sub-1@node", NodeUUID: "11111111-2222-3333-4444-555555555555"},
		{Key: "subuser:su-1", NodeUUID: "11111111-2222-3333-4444-555555555555"},
		{Key: "subuser:su-1", Email: "u-sub-1@node"},
		{Key: "subuser:su-1", Email: "u-sub-1@node", NodeUUID: "11111111-2222-3333-4444-555555555555", LimitBytes: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("缺字段的用户行必须校验失败：%+v", bad)
		}
	}
}
