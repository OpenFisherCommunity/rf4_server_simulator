package aibridge

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterKeyStableAndEncryptRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "ai_master.key")
	key1, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("生成主密钥: %v", err)
	}
	key2, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("再次读取主密钥: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("主密钥重复读取不一致")
	}
	plain := []byte("sk-this-is-a-secret")
	ct, nonce, err := Encrypt(key1, plain)
	if err != nil {
		t.Fatalf("加密: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("密文中出现明文")
	}
	got, err := Decrypt(key2, ct, nonce)
	if err != nil {
		t.Fatalf("解密: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密结果 = %q", got)
	}
}

func TestMaskedHidesKey(t *testing.T) {
	got := Masked("sk-abcdefgh1234")
	if strings.Contains(got, "abcdefgh") {
		t.Fatalf("masked 泄露中段: %s", got)
	}
	if !strings.HasSuffix(got, "1234") {
		t.Fatalf("masked 应保留尾四位: %s", got)
	}
	if Masked("ab") != "****" {
		t.Fatal("短密钥应完全遮蔽")
	}
}
