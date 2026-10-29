// Package sysconfig owns the console-managed system parameters (admin
// console「系统参数」page): the R2 targets for media and backups. Values live
// in the settings table; media falls back field-by-field to the process
// environment (CORE_R2_*) so existing deployments keep working until the
// operator moves them into the console. Backup has no env fallback — the env
// vars point at the public media bucket, which must never receive snapshots.
package sysconfig

import (
	"fmt"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
)

// SettingSource reads and writes console-persisted settings; satisfied by
// *store.Store and by fakes in tests.
type SettingSource interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

// Settings-table keys for the media (public) R2 target.
const (
	SettingKeyMediaEndpoint   = "media_r2_endpoint"
	SettingKeyMediaKeyID      = "media_r2_key_id"
	SettingKeyMediaSecret     = "media_r2_secret"
	SettingKeyMediaBucket     = "media_r2_bucket"
	SettingKeyMediaPublicBase = "media_r2_public_base"
)

// Settings-table keys for the backup (private) R2 target.
const (
	SettingKeyBackupEndpoint = "backup_r2_endpoint"
	SettingKeyBackupKeyID    = "backup_r2_key_id"
	SettingKeyBackupSecret   = "backup_r2_secret"
	SettingKeyBackupBucket   = "backup_r2_bucket"
)

// MediaR2 is the console-managed media (public bucket) configuration.
type MediaR2 struct {
	Endpoint   string
	KeyID      string
	Secret     string
	Bucket     string
	PublicBase string // public URL prefix, baked into stored media URLs at upload
}

// Complete reports whether every field is set. The public base is required:
// without it uploads produce unreachable URLs that get persisted.
func (c MediaR2) Complete() bool {
	return c.Endpoint != "" && c.KeyID != "" && c.Secret != "" &&
		c.Bucket != "" && c.PublicBase != ""
}

// BackupR2 is the console-managed backup (private bucket) configuration.
// The secret is stored plaintext (no KMS to wrap it with) — the backup
// bucket must therefore stay strictly private; the console never renders it.
type BackupR2 struct {
	Endpoint string
	KeyID    string
	Secret   string
	Bucket   string
}

// Complete reports whether every field is set — the minimum for backups
// (and connection tests) to run.
func (c BackupR2) Complete() bool {
	return c.Endpoint != "" && c.KeyID != "" && c.Secret != "" && c.Bucket != ""
}

// LoadMediaR2 reads the stored media config (DB values only, no fallback).
func LoadMediaR2(src SettingSource) (MediaR2, error) {
	var c MediaR2
	var err error
	read := func(key string, dst *string) bool {
		if *dst, err = src.GetSetting(key); err != nil {
			err = fmt.Errorf("load %s: %w", key, err)
			return false
		}
		return true
	}
	if !read(SettingKeyMediaEndpoint, &c.Endpoint) ||
		!read(SettingKeyMediaKeyID, &c.KeyID) ||
		!read(SettingKeyMediaSecret, &c.Secret) ||
		!read(SettingKeyMediaBucket, &c.Bucket) ||
		!read(SettingKeyMediaPublicBase, &c.PublicBase) {
		return MediaR2{}, err
	}
	return c, nil
}

// SaveMediaR2 persists the media config.
func SaveMediaR2(src SettingSource, c MediaR2) error {
	return save(src, map[string]string{
		SettingKeyMediaEndpoint:   c.Endpoint,
		SettingKeyMediaKeyID:      c.KeyID,
		SettingKeyMediaSecret:     c.Secret,
		SettingKeyMediaBucket:     c.Bucket,
		SettingKeyMediaPublicBase: c.PublicBase,
	})
}

// LoadBackupR2 reads the stored backup config.
func LoadBackupR2(src SettingSource) (BackupR2, error) {
	var c BackupR2
	var err error
	read := func(key string, dst *string) bool {
		if *dst, err = src.GetSetting(key); err != nil {
			err = fmt.Errorf("load %s: %w", key, err)
			return false
		}
		return true
	}
	if !read(SettingKeyBackupEndpoint, &c.Endpoint) ||
		!read(SettingKeyBackupKeyID, &c.KeyID) ||
		!read(SettingKeyBackupSecret, &c.Secret) ||
		!read(SettingKeyBackupBucket, &c.Bucket) {
		return BackupR2{}, err
	}
	return c, nil
}

// SaveBackupR2 persists the backup config.
func SaveBackupR2(src SettingSource, c BackupR2) error {
	return save(src, map[string]string{
		SettingKeyBackupEndpoint: c.Endpoint,
		SettingKeyBackupKeyID:    c.KeyID,
		SettingKeyBackupSecret:   c.Secret,
		SettingKeyBackupBucket:   c.Bucket,
	})
}

func save(src SettingSource, kv map[string]string) error {
	for key, v := range kv {
		if err := src.SetSetting(key, v); err != nil {
			return fmt.Errorf("save %s: %w", key, err)
		}
	}
	return nil
}

// EffectiveMediaR2 resolves the effective media config field by field: a
// database value wins when set, the environment fills the gaps. Pure so the
// settings page can render sources and the client factory can share it.
func EffectiveMediaR2(db, env MediaR2) MediaR2 {
	each := func(d, e string) string {
		if d != "" {
			return d
		}
		return e
	}
	return MediaR2{
		Endpoint:   each(db.Endpoint, env.Endpoint),
		KeyID:      each(db.KeyID, env.KeyID),
		Secret:     each(db.Secret, env.Secret),
		Bucket:     each(db.Bucket, env.Bucket),
		PublicBase: each(db.PublicBase, env.PublicBase),
	}
}

// MediaClientFactory builds a media client factory over the settings table
// plus the environment fallback: nil (uploads disabled) until the effective
// config is complete. Re-resolved per call, so console edits apply without a
// restart. build exists so tests can inject a fake client.
func MediaClientFactory(src SettingSource, env MediaR2, build func(MediaR2) s3store.Client) func() s3store.Client {
	return func() s3store.Client {
		db, err := LoadMediaR2(src)
		if err != nil {
			return nil
		}
		eff := EffectiveMediaR2(db, env)
		if !eff.Complete() {
			return nil
		}
		return build(eff)
	}
}

// BackupClientFactory builds the backup client factory (settings only — no
// env fallback by design). Nil until the console config is complete.
func BackupClientFactory(src SettingSource, build func(BackupR2) s3store.Client) func() s3store.Client {
	return func() s3store.Client {
		c, err := LoadBackupR2(src)
		if err != nil || !c.Complete() {
			return nil
		}
		return build(c)
	}
}
