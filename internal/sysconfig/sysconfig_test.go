package sysconfig

import (
	"strings"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

func newSettingStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(newBackupDB(t))
}

func TestMediaR2RoundTrip(t *testing.T) {
	st := newSettingStore(t)
	want := MediaR2{
		Endpoint:   "https://acc.r2.cloudflarestorage.com",
		KeyID:      "kid",
		Secret:     "sec",
		Bucket:     "blog",
		PublicBase: "https://media.example.com",
	}
	if err := SaveMediaR2(st, want); err != nil {
		t.Fatalf("SaveMediaR2: %v", err)
	}
	got, err := LoadMediaR2(st)
	if err != nil {
		t.Fatalf("LoadMediaR2: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !got.Complete() {
		t.Error("Complete() = false for fully set media config")
	}
}

func TestEffectiveMediaR2PrefersDBPerField(t *testing.T) {
	env := MediaR2{
		Endpoint: "https://env-endpoint", KeyID: "env-kid", Secret: "env-sec",
		Bucket: "env-bucket", PublicBase: "https://env-media",
	}
	db := MediaR2{Endpoint: "https://db-endpoint", Bucket: "db-bucket"}

	got := EffectiveMediaR2(db, env)
	want := MediaR2{
		Endpoint: "https://db-endpoint", KeyID: "env-kid", Secret: "env-sec",
		Bucket: "db-bucket", PublicBase: "https://env-media",
	}
	if got != want {
		t.Errorf("effective = %+v, want %+v", got, want)
	}
	// Neither side set → empty field stays empty.
	if got := EffectiveMediaR2(MediaR2{}, MediaR2{}); got.Complete() {
		t.Error("empty db+env must not be complete")
	}
}

func TestMediaClientFactoryResolvesPerCall(t *testing.T) {
	st := newSettingStore(t)
	env := MediaR2{
		Endpoint: "https://env-endpoint", KeyID: "env-kid", Secret: "env-sec",
		Bucket: "env-bucket", PublicBase: "https://env-media",
	}

	var resolved MediaR2
	client := &s3store.Fake{}
	factory := MediaClientFactory(st, env, func(c MediaR2) s3store.Client {
		resolved = c
		return client
	})

	// DB empty → env fallback in effect.
	if c := factory(); c == nil {
		t.Fatal("factory() = nil with complete env fallback")
	}
	if resolved.Bucket != "env-bucket" {
		t.Errorf("resolved bucket = %q, want env value", resolved.Bucket)
	}

	// Operator overrides just the bucket in the console → next call uses it,
	// other fields still env-sourced.
	if err := st.SetSetting(SettingKeyMediaBucket, "db-bucket"); err != nil {
		t.Fatal(err)
	}
	if c := factory(); c == nil {
		t.Fatal("factory() = nil after db override")
	}
	if resolved.Bucket != "db-bucket" || resolved.Endpoint != "https://env-endpoint" {
		t.Errorf("resolved = %+v, want db bucket + env endpoint", resolved)
	}

	// Nothing configured at all → nil (media upload disabled).
	empty := MediaClientFactory(st, MediaR2{}, func(MediaR2) s3store.Client { return client })
	if err := SaveMediaR2(st, MediaR2{}); err != nil {
		t.Fatal(err)
	}
	if c := empty(); c != nil {
		t.Error("factory() != nil with nothing configured")
	}
}

func TestBackupR2RoundTrip(t *testing.T) {
	st := newSettingStore(t)
	want := BackupR2{
		Endpoint: "https://acc.r2.cloudflarestorage.com",
		KeyID:    "kid",
		Secret:   "sec",
		Bucket:   "core-backups",
	}
	if err := SaveBackupR2(st, want); err != nil {
		t.Fatalf("SaveBackupR2: %v", err)
	}
	got, err := LoadBackupR2(st)
	if err != nil {
		t.Fatalf("LoadBackupR2: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !got.Complete() {
		t.Error("Complete() = false for fully set backup config")
	}
	// Backup has no env fallback by design (env vars point at the public
	// media bucket); dropping one field must disable it.
	partial := want
	partial.Secret = ""
	if partial.Complete() {
		t.Error("Complete() = true with missing secret")
	}
}

func TestBackupClientFactory(t *testing.T) {
	st := newSettingStore(t)
	client := &s3store.Fake{}
	factory := BackupClientFactory(st, func(BackupR2) s3store.Client { return client })

	if c := factory(); c != nil {
		t.Error("factory() != nil with empty settings")
	}
	if err := SaveBackupR2(st, BackupR2{
		Endpoint: "https://acc.r2.cloudflarestorage.com",
		KeyID:    "kid", Secret: "sec", Bucket: "core-backups",
	}); err != nil {
		t.Fatal(err)
	}
	if c := factory(); c == nil {
		t.Error("factory() = nil with complete settings")
	}
}

// ── 统计站点（多站打点域名白名单）──────────────────────────────

func TestParseStatsSitesNormalizes(t *testing.T) {
	got := ParseStatsSites("https://ask.xiaowenz.com, card.xiaowenz.com\n\n money.xiaowenz.com , ask.xiaowenz.com  ")
	want := []string{"ask.xiaowenz.com", "card.xiaowenz.com", "money.xiaowenz.com"}
	if len(got) != len(want) {
		t.Fatalf("ParseStatsSites = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q（去 scheme、小写、去重、保序）", i, got[i], want[i])
		}
	}
	if ParseStatsSites("  \n , ") != nil {
		t.Error("blank input must parse to nil")
	}
}

func TestStatsSitesRoundTrip(t *testing.T) {
	st := newSettingStore(t)
	want := []string{"ask.xiaowenz.com", "card.xiaowenz.com"}
	if err := SaveStatsSites(st, want); err != nil {
		t.Fatalf("SaveStatsSites: %v", err)
	}
	got, err := LoadStatsSites(st)
	if err != nil {
		t.Fatalf("LoadStatsSites: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// 未配置时返回空列表（打点只认 CORE_SITE_URL 本站），不报错。
func TestStatsSitesEmpty(t *testing.T) {
	st := newSettingStore(t)
	got, err := LoadStatsSites(st)
	if err != nil {
		t.Fatalf("LoadStatsSites unconfigured: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unconfigured = %v, want empty", got)
	}
}

// 个人介绍（Newsletter 页脚「我是谁」区块）：round-trip 保多行原文。
func TestNewsletterBioRoundTrip(t *testing.T) {
	st := newSettingStore(t)
	want := "我是晓文，写代码也写字。\n在一家小公司做技术负责人。\n博客：三杯过后。"
	if err := SaveNewsletterBio(st, want); err != nil {
		t.Fatalf("SaveNewsletterBio: %v", err)
	}
	got, err := LoadNewsletterBio(st)
	if err != nil {
		t.Fatalf("LoadNewsletterBio: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %q, want %q", got, want)
	}
}

// Effective 回退：数据库配置优先，空则回退 env（CORE_NEWSLETTER_BIO）。
func TestEffectiveNewsletterBio(t *testing.T) {
	if got := EffectiveNewsletterBio("来自后台", "来自env"); got != "来自后台" {
		t.Errorf("db 优先失败: %q", got)
	}
	if got := EffectiveNewsletterBio("", "来自env"); got != "来自env" {
		t.Errorf("env 回退失败: %q", got)
	}
	if got := EffectiveNewsletterBio("", ""); got != "" {
		t.Errorf("双空应为空: %q", got)
	}
}

// 超长个人介绍整单拒绝（防邮件爆炸，上限 2000 字符）。
func TestNewsletterBioTooLongRejected(t *testing.T) {
	st := newSettingStore(t)
	long := strings.Repeat("字", 2001)
	if err := SaveNewsletterBio(st, long); err == nil {
		t.Fatal("超长 bio 必须报错")
	}
	got, _ := LoadNewsletterBio(st)
	if got != "" {
		t.Errorf("被拒绝的值不应落库: %q", got)
	}
}

// 保存时归一化：首尾空白剔除，中间换行保留。
func TestNewsletterBioNormalized(t *testing.T) {
	st := newSettingStore(t)
	if err := SaveNewsletterBio(st, "  你好。\n我是晓文。 \n"); err != nil {
		t.Fatalf("SaveNewsletterBio: %v", err)
	}
	got, _ := LoadNewsletterBio(st)
	if got != "你好。\n我是晓文。" {
		t.Errorf("归一化结果 = %q", got)
	}
}
