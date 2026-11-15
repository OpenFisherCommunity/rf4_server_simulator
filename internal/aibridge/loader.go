package aibridge

// 从数据库加载厂商配置，供 Runner 与后台「测试连接」共用，避免两处各写一套解密逻辑。

import (
	"context"
	"strings"
	"time"

	"rf4server/internal/store"
)

// LoadSpecFromDB 读取启用的厂商、模型与（解密后的）API Key。
func LoadSpecFromDB(ctx context.Context, db *store.DB, masterKey []byte) (ProviderSpec, error) {
	var spec ProviderSpec
	var ciphertext, nonce []byte
	row := db.SQL.QueryRowContext(ctx, `
		SELECT p.name, p.protocol, p.base_url, p.auth_scheme, p.config_json,
		       m.model_id, s.ciphertext, s.nonce
		FROM ai_provider p
		JOIN ai_model m ON m.provider_id=p.id AND m.enabled=1
		LEFT JOIN ai_secret s ON s.provider_id=p.id
		WHERE p.enabled=1
		ORDER BY p.id LIMIT 1`)
	if err := row.Scan(&spec.Name, &spec.Protocol, &spec.BaseURL, &spec.AuthScheme,
		&spec.ConfigJSON, &spec.ModelID, &ciphertext, &nonce); err != nil {
		return spec, err
	}
	if len(ciphertext) > 0 && len(nonce) > 0 && masterKey != nil {
		plain, err := Decrypt(masterKey, ciphertext, nonce)
		if err != nil {
			return spec, err
		}
		spec.APIKey = strings.TrimSpace(string(plain))
	}
	return spec, nil
}

// LoadProviderFromDB 直接构造可用 Provider。
func LoadProviderFromDB(ctx context.Context, db *store.DB, masterKey []byte, timeout time.Duration) (Provider, error) {
	spec, err := LoadSpecFromDB(ctx, db, masterKey)
	if err != nil {
		return nil, err
	}
	return NewProvider(spec, timeout)
}
