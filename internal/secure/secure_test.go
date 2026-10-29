package secure

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func testKey() []byte {
	k := make([]byte, 32)
	rand.Read(k)
	return k
}

func TestEncryptDecrypt(t *testing.T) {
	key := testKey()
	for _, plain := range []string{"", "p@ssw0rd", "-----BEGIN KEY-----\nabc\n-----END KEY-----", "中文密码🔐"} {
		enc, err := Encrypt(key, plain)
		if err != nil {
			t.Fatalf("加密失败: %v", err)
		}
		dec, err := Decrypt(key, enc)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		if dec != plain {
			t.Fatalf("往返不一致: %q -> %q", plain, dec)
		}
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	enc, _ := Encrypt(testKey(), "secret")
	if _, err := Decrypt(testKey(), enc); err == nil {
		t.Fatal("错误密钥不应解密成功")
	}
}

func TestTOTP(t *testing.T) {
	secret := NewTOTPSecret()
	if len(secret) < 16 {
		t.Fatalf("密钥过短: %s", secret)
	}
	code := totpAt(secret, time.Now())
	if !VerifyTOTP(secret, code) {
		t.Fatal("当前时间窗的验证码应当通过")
	}
	if VerifyTOTP(secret, "000000") && code != "000000" {
		t.Fatal("错误验证码不应通过")
	}
	if !strings.HasPrefix(TOTPProvisionURL("Warden", "admin", secret), "otpauth://totp/") {
		t.Fatal("otpauth 链接格式错误")
	}
}
