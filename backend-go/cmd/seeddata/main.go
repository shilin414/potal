// seeddata — one-time data migration from the retired Django reference
// database (xiaoan3) into the clean Go schema (xiaoan3_go). The Django stack
// has been removed (G10); this tool remains for replaying the original
// migration while a source snapshot is still reachable. It is not part of
// the runtime or the test baseline.
//
// Copies: application_categories, users, feishu_identities,
// applications, runtime_bindings, application_favorites.
//
// Rules:
//   - Numeric ids are preserved so FKs and any external references stay
//     consistent.
//   - Django PBKDF2 password hashes are copied verbatim (VerifyLocalAdmin
//     understands them); new passwords will be Argon2id.
//   - Feishu refresh tokens are read from the source (the reference stored
//     them plaintext when IDENTITY_TOKEN_ENCRYPTION_KEY was unset) and
//     re-encrypted with the Go backend's AES-256-GCM key.
//   - Django 32-hex UUID ids (runtime_bindings) become BINARY(16).
//   - Application avatar files are copied from the Django MEDIA_ROOT into
//     the configured Storage.
//
// Usage:
//
//	go run ./cmd/seeddata \
//	  --source-dsn 'user:pass@tcp(host:port)/xiaoan3?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  --media-root ../backend/media
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/creation-agent-studio/backend-go/internal/app"
	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
	"github.com/creation-agent-studio/backend-go/internal/platform/storage"
)

func main() {
	sourceDSN := flag.String("source-dsn", os.Getenv("SEED_SOURCE_DSN"), "DSN of the source (Django) database")
	mediaRoot := flag.String("media-root", os.Getenv("SEED_MEDIA_ROOT"), "Django MEDIA_ROOT for avatar files")
	force := flag.Bool("force", false, "wipe previously seeded rows and re-seed")
	flag.Parse()

	logger := slog.Default()

	aCfg, err := app.LoadConfig()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	a, err := app.Build(context.Background(), aCfg)
	if err != nil {
		logger.Error("build app (target)", "err", err)
		os.Exit(1)
	}
	defer a.Close()

	if *sourceDSN == "" {
		logger.Error("--source-dsn (or SEED_SOURCE_DSN) is required")
		os.Exit(1)
	}
	src, err := sql.Open("mysql", *sourceDSN)
	if err != nil {
		logger.Error("open source", "err", err)
		os.Exit(1)
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		logger.Error("ping source", "err", err)
		os.Exit(1)
	}

	gcm, err := crypto.NewAESGCM(aCfg.Auth.TokenEncryptionKey)
	if err != nil {
		logger.Error("encryption key", "err", err)
		os.Exit(1)
	}
	_ = a.Storage

	s := &seeder{src: src, dst: a.DB, gcm: gcm, mediaRoot: *mediaRoot, log: logger, storage: a.Storage}
	if err := s.run(context.Background(), *force); err != nil {
		logger.Error("seed failed", "err", err)
		os.Exit(1)
	}
	logger.Info("seed complete")
}

type seeder struct {
	src       *sql.DB
	dst       *sql.DB
	gcm       *crypto.AESGCM
	mediaRoot string
	storage   storage.Storage
	log       *slog.Logger
}

func (s *seeder) run(ctx context.Context, force bool) error {
	dst := s.dst

	// Idempotency: refuse to double-seed unless --force.
	var users, apps int
	if err := dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if err := dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications`).Scan(&apps); err != nil {
		return err
	}
	if users > 0 || apps > 0 {
		if !force {
			return fmt.Errorf("target already has data (users=%d applications=%d); use --force to wipe and re-seed", users, apps)
		}
		s.log.Warn("force: wiping previously seeded rows")
		for _, stmt := range []string{
			`SET FOREIGN_KEY_CHECKS=0`,
			`DELETE FROM application_favorites`,
			`DELETE FROM runtime_bindings`,
			`DELETE FROM applications`,
			`DELETE FROM feishu_identities`,
			`DELETE FROM application_categories`,
			`DELETE FROM users`,
			`SET FOREIGN_KEY_CHECKS=1`,
		} {
			if _, err := dst.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("wipe: %w", err)
			}
		}
	}

	if err := s.seedCategories(ctx); err != nil {
		return fmt.Errorf("categories: %w", err)
	}
	n, err := s.seedUsers(ctx)
	if err != nil {
		return fmt.Errorf("users: %w", err)
	}
	s.log.Info("users seeded", "count", n)
	n, err = s.seedFeishuIdentities(ctx)
	if err != nil {
		return fmt.Errorf("feishu_identities: %w", err)
	}
	s.log.Info("feishu identities seeded", "count", n)
	n, err = s.seedApplications(ctx)
	if err != nil {
		return fmt.Errorf("applications: %w", err)
	}
	s.log.Info("applications seeded", "count", n)
	n, err = s.seedBindings(ctx)
	if err != nil {
		return fmt.Errorf("runtime_bindings: %w", err)
	}
	s.log.Info("runtime bindings seeded", "count", n)
	n, err = s.seedFavorites(ctx)
	if err != nil {
		return fmt.Errorf("favorites: %w", err)
	}
	s.log.Info("favorites seeded", "count", n)
	return nil
}

func (s *seeder) seedCategories(ctx context.Context) error {
	rows, err := s.src.QueryContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''), COALESCE(icon,''), COALESCE(`+"`order`"+`,0) FROM application_categories`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug, name, desc, icon string
		var order int
		if err := rows.Scan(&id, &slug, &name, &desc, &icon, &order); err != nil {
			return err
		}
		_, err = s.dst.ExecContext(ctx,
			`INSERT INTO application_categories (id, slug, name, description, icon, sort_order) VALUES (?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE name = VALUES(name)`,
			id, slug, name, desc, icon, order)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *seeder) seedUsers(ctx context.Context) (int, error) {
	rows, err := s.src.QueryContext(ctx,
		`SELECT id, username, password, COALESCE(first_name,''), COALESCE(last_name,''), email,
		        is_staff, is_active, role, COALESCE(auth_source,''), COALESCE(avatar,''), COALESCE(bio,''),
		        COALESCE(display_id,''), COALESCE(date_joined, created_at), updated_at
		 FROM users`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			id                     int64
			username, password     string
			firstName, lastName    string
			email                  string
			isStaff, isActive      bool
			role, authSource       string
			avatar, bio, displayID string
			createdAt, updatedAt   sql.NullTime
		)
		if err := rows.Scan(&id, &username, &password, &firstName, &lastName, &email,
			&isStaff, &isActive, &role, &authSource, &avatar, &bio, &displayID,
			&createdAt, &updatedAt); err != nil {
			return count, err
		}
		// display_name preference: feishu display name → first_name → username.
		displayName := firstName
		if displayName == "" {
			displayName = lastName
		}
		if displayName == "" {
			displayName = username
		}
		_, err := s.dst.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, display_name, display_id, email, avatar_url, bio, role, auth_source, is_staff, is_active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE username = VALUES(username)`,
			id, username, password, displayName, displayID, email, avatar, bio, role, authSource,
			isStaff, isActive, createdAt, updatedAt)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (s *seeder) seedFeishuIdentities(ctx context.Context) (int, error) {
	rows, err := s.src.QueryContext(ctx,
		`SELECT user_id, COALESCE(feishu_user_id,''), COALESCE(open_id,''), COALESCE(union_id,''),
		        COALESCE(display_name,''), COALESCE(avatar_url,''), COALESCE(refresh_token,''),
		        refresh_token_expires_at, last_login_at, created_at, updated_at
		 FROM identity_feishu_identities`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			userID                    int64
			fuID, openID, unionID     string
			displayName, avatarURL    string
			refreshToken              string
			refreshExpires, lastLogin sql.NullTime
			createdAt, updatedAt      sql.NullTime
		)
		if err := rows.Scan(&userID, &fuID, &openID, &unionID, &displayName, &avatarURL,
			&refreshToken, &refreshExpires, &lastLogin, &createdAt, &updatedAt); err != nil {
			return count, err
		}
		// Re-encrypt the refresh token with the Go backend's key.
		enc, err := s.gcm.Encrypt(refreshToken)
		if err != nil {
			return count, fmt.Errorf("encrypt refresh token for user %d: %w", userID, err)
		}
		_, err = s.dst.ExecContext(ctx,
			`INSERT INTO feishu_identities (user_id, open_id, union_id, feishu_user_id, display_name, avatar_url, refresh_token_enc, refresh_token_expires_at, last_login_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE refresh_token_enc = VALUES(refresh_token_enc), refresh_token_expires_at = VALUES(refresh_token_expires_at)`,
			userID, nullStr(openID), unionID, nullStr(fuID), displayName, avatarURL, enc,
			refreshExpires, lastLogin, createdAt, updatedAt)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (s *seeder) seedApplications(ctx context.Context) (int, error) {
	rows, err := s.src.QueryContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''), COALESCE(icon,''), COALESCE(color,''),
		        COALESCE(kind,'chat'), COALESCE(renderer_key,''), COALESCE(executor_key,''),
		        category_id, is_public, is_default_agent, usage_count, created_by_id,
		        created_at, updated_at, COALESCE(avatar,'')
		 FROM applications`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			id                             int64
			slug, name, desc, icon, color  string
			kind, rendererKey, executorKey string
			categoryID, createdBy          sql.NullInt64
			isPublic, isDefaultAgent       bool
			usageCount                     int64
			createdAt, updatedAt           sql.NullTime
			avatarPath                     string
		)
		if err := rows.Scan(&id, &slug, &name, &desc, &icon, &color, &kind, &rendererKey,
			&executorKey, &categoryID, &isPublic, &isDefaultAgent, &usageCount,
			&createdBy, &createdAt, &updatedAt, &avatarPath); err != nil {
			return count, err
		}
		_, err := s.dst.ExecContext(ctx,
			`INSERT INTO applications (id, slug, name, description, icon, avatar_key, color, kind, renderer_key, executor_key,
			    category_id, is_public, is_default_agent, usage_count, created_by, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE name = VALUES(name)`,
			id, slug, name, desc, icon, avatarPath, color, kind, rendererKey, executorKey,
			categoryID, isPublic, isDefaultAgent, usageCount, createdBy, createdAt, updatedAt)
		if err != nil {
			return count, err
		}
		// Copy the avatar file from the Django media root into Storage.
		if avatarPath != "" && s.mediaRoot != "" {
			if err := s.copyMedia(ctx, avatarPath); err != nil {
				s.log.Warn("avatar copy failed", "path", avatarPath, "err", err)
			}
		}
		count++
	}
	return count, rows.Err()
}

func (s *seeder) copyMedia(ctx context.Context, relPath string) error {
	if strings.Contains(relPath, "..") {
		return fmt.Errorf("unsafe media path")
	}
	full := s.mediaRoot + "/" + strings.ReplaceAll(relPath, "\\", "/")
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	contentType := "application/octet-stream"
	switch {
	case strings.HasSuffix(relPath, ".png"):
		contentType = "image/png"
	case strings.HasSuffix(relPath, ".jpg"), strings.HasSuffix(relPath, ".jpeg"):
		contentType = "image/jpeg"
	}
	_, err = s.storage.Put(ctx, relPath, bytes.NewReader(data), contentType)
	return err
}

func (s *seeder) seedBindings(ctx context.Context) (int, error) {
	rows, err := s.src.QueryContext(ctx,
		`SELECT id, application_id, provider_id, provider_key, runtime_type, COALESCE(external_resource_id,''),
		        COALESCE(endpoint_key,''), COALESCE(identity_mode,'user'), COALESCE(execution_mode,'interactive'),
		        COALESCE(session_policy,'lazy'), COALESCE(artifact_policy,'external_refresh'),
		        capabilities, config, secret_ref, timeout_seconds, enabled, created_at, updated_at
		 FROM catalog_runtime_bindings`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			rawID                                        string
			applicationID                                int64
			providerID                                   sql.NullString
			providerKey, runtimeType, externalResourceID string
			endpointKey, identityMode, executionMode     string
			sessionPolicy, artifactPolicy, secretRef     string
			capabilities, config                         sql.NullString
			timeoutSeconds                               int64
			enabled                                      bool
			createdAt, updatedAt                         sql.NullTime
		)
		if err := rows.Scan(&rawID, &applicationID, &providerID, &providerKey, &runtimeType,
			&externalResourceID, &endpointKey, &identityMode, &executionMode, &sessionPolicy,
			&artifactPolicy, &capabilities, &config, &secretRef, &timeoutSeconds, &enabled,
			&createdAt, &updatedAt); err != nil {
			return count, err
		}
		_ = rawID // Django UUID ids are not carried over: the clean schema
		// exposes runtime_binding ids as BIGINT (API contract) and no
		// external reference depends on the old UUIDs.
		// Resolve the provider row in the target by key (provider ids
		// differ between systems; keys do not).
		var newProviderID sql.NullInt64
		if providerID.Valid {
			var pid int64
			err := s.dst.QueryRowContext(ctx, `SELECT id FROM providers WHERE provider_key = ?`, providerKey).Scan(&pid)
			if err == nil {
				newProviderID = sql.NullInt64{Int64: pid, Valid: true}
			} else {
				s.log.Warn("provider key not found in target; binding keeps key only", "provider_key", providerKey)
			}
		}
		_, err = s.dst.ExecContext(ctx,
			`INSERT INTO runtime_bindings (application_id, provider_id, provider_key, runtime_type, external_resource_id,
			    endpoint_key, identity_mode, execution_mode, session_policy, artifact_policy, capabilities, config, secret_ref,
			    timeout_seconds, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE external_resource_id = VALUES(external_resource_id), enabled = VALUES(enabled)`,
			applicationID, newProviderID, providerKey, runtimeType, externalResourceID,
			endpointKey, identityMode, executionMode, sessionPolicy, artifactPolicy,
			capabilities, config, secretRef, timeoutSeconds, enabled, createdAt, updatedAt)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (s *seeder) seedFavorites(ctx context.Context) (int, error) {
	rows, err := s.src.QueryContext(ctx, `SELECT user_id, application_id, created_at FROM application_favorites`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var userID, appID int64
		var createdAt sql.NullTime
		if err := rows.Scan(&userID, &appID, &createdAt); err != nil {
			return count, err
		}
		if _, err := s.dst.ExecContext(ctx,
			`INSERT IGNORE INTO application_favorites (user_id, application_id, created_at) VALUES (?, ?, ?)`,
			userID, appID, createdAt); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

// hexUUID converts the Django 32-hex UUID form to BINARY(16).
func hexUUID(s string) ([]byte, error) {
	clean := strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), " ", "")
	if len(clean) != 32 {
		return nil, fmt.Errorf("not a 32-hex uuid")
	}
	return hex.DecodeString(clean)
}

func nullStr(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}
