package sysconfig

import (
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
