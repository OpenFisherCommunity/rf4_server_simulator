package admin

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeAIErrorMasksKey(t *testing.T) {
	got := safeAIError(errors.New("厂商返回 401: sk-abcdefgh123456 unauthorized"))
	if strings.Contains(got, "abcdefgh") {
		t.Fatalf("错误信息泄露密钥: %q", got)
	}
	if !strings.Contains(got, "sk-***") {
		t.Fatalf("未替换为掩码: %q", got)
	}
	if safeAIError(nil) != "" {
		t.Fatal("nil 错误应返回空")
	}
}

func TestValidBaseURL(t *testing.T) {
	for _, ok := range []string{"https://api.deepseek.com", "http://127.0.0.1:11434/v1"} {
		if !validBaseURL(ok) {
			t.Fatalf("合法地址被拒绝: %s", ok)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "api.deepseek.com", "ftp://x"} {
		if validBaseURL(bad) {
			t.Fatalf("非法地址被接受: %s", bad)
		}
	}
}

func TestSafeAIErrorTruncates(t *testing.T) {
	long := strings.Repeat("错误信息", 100)
	got := safeAIError(errors.New(long))
	if len([]rune(got)) > 200 {
		t.Fatalf("未截断: %d", len([]rune(got)))
	}
}
