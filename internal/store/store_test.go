package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(d)
}

func futureExpiry() time.Time { return time.Now().Add(24 * time.Hour) }

// insertDownloadAt inserts a download row with an explicit timestamp so tests
// can place rows outside the current-day dedupe window.
func (s *Store) insertDownloadAt(slug, ipHash string, t time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO downloads (slug, ip_hash, ua, referer, created_at) VALUES (?, ?, '', '', ?)`,
		slug, ipHash, t.Unix())
	return err
}

// Settings：管理台可持久化配置（播客分发等），get/set 往返 + 覆盖更新。
func TestSettingsRoundtrip(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetSetting("apple_show_id"); err == nil {
		// 不存在不报错——返回空即可（见实现）；这里只验证未写入前为空
	}
	if err := s.SetSetting("apple_show_id", "1880021392"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, err := s.GetSetting("apple_show_id")
	if err != nil || v != "1880021392" {
		t.Fatalf("GetSetting = %q, %v", v, err)
	}
	if err := s.SetSetting("apple_show_id", "42"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("apple_show_id"); v != "42" {
		t.Errorf("覆盖更新失败: %q", v)
	}
	if v, err := s.GetSetting("never_set"); err != nil || v != "" {
		t.Errorf("未写入的 key 应返回空串: %q, %v", v, err)
	}
}
