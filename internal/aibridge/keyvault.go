package aibridge

// API Key 加密存储：AES-GCM + 本地主密钥文件。明文不落库、不进日志、不进配置。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const masterKeySize = 32

// LoadOrCreateMasterKey 读取或生成 32 字节主密钥，仅保存在运行版本地。
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != masterKeySize {
			return nil, fmt.Errorf("ai: 主密钥长度异常 (%d)", len(b))
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	if len(key) != masterKeySize {
		return nil, errors.New("ai: 主密钥长度错误")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt 返回密文与 nonce。
func Encrypt(key, plaintext []byte) ([]byte, []byte, error) {
	g, err := gcmFor(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return g.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Decrypt 解密后台保存的 Key。
func Decrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	g, err := gcmFor(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != g.NonceSize() {
		return nil, errors.New("ai: nonce 长度错误")
	}
	return g.Open(nil, nonce, ciphertext, nil)
}

// Masked 只保留尾四位用于后台展示。
func Masked(plain string) string {
	plain = strings.TrimSpace(plain)
	if len(plain) <= 4 {
		return "****"
	}
	return "sk-****" + plain[len(plain)-4:]
}
