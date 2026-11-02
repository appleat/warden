package rediscli

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Redis 的键名是二进制安全的字节串，但面板要经 JSON 传到浏览器再传回来。
// encoding/json 遇到非法 UTF-8 字节会统一替换成 U+FFFD（�），
// 于是 Java 序列化这类二进制键在页面上显示成乱码，再拿这个「乱码字符串」
// 去查键自然就是「键不存在」。所以列表与详情都同时返回：
//   - key：转义过的展示名（\xNN 形式，可打印 ASCII 保留，类名等仍可读）
//   - key_raw：原始字节的 base64，供回传时无损还原
// 只有确实需要转义的键才带 key_raw，普通键的 JSON 不会多出冗余字段。

// needsEscape 判断键名能否原样放进 JSON 并正常显示：
// 非法 UTF-8 与控制字符（含 NUL）都不行；反斜杠也要转义，否则
// 真实键名里的「\x41」和转义出来的「\x41」无法区分。
func needsEscape(key string) bool {
	for i := 0; i < len(key); {
		c := key[i]
		if c == '\\' || c < 0x20 || c == 0x7f {
			return true
		}
		if c < 0x80 {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(key[i:])
		if r == utf8.RuneError && size == 1 {
			return true
		}
		i += size
	}
	return false
}

// KeyDisplay 返回键名的展示形式。普通键原样返回；含二进制字节的键逐字节转义，
// 可打印 ASCII 与合法 UTF-8（中文键名）保持不变。
func KeyDisplay(key string) string {
	if key == "" || !needsEscape(key) {
		return key
	}
	var b strings.Builder
	b.Grow(len(key) + 8)
	for i := 0; i < len(key); {
		c := key[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
			i++
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
			i++
		case c >= 0x80:
			r, size := utf8.DecodeRuneInString(key[i:])
			if r == utf8.RuneError && size == 1 {
				fmt.Fprintf(&b, `\x%02x`, c)
				i++
				continue
			}
			b.WriteString(key[i : i+size])
			i += size
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
			i++
		}
	}
	return b.String()
}

// KeyRaw 返回原始字节的 base64；普通键返回空串（前端直接用 key 即可）。
func KeyRaw(key string) string {
	if !needsEscape(key) {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(key))
}

// DecodeKey 还原请求里的键名：给了 keyRaw 就按 base64 解出原始字节，
// 否则用明文键名（顺手去掉首尾空白，与旧行为一致）。
// 二进制键必须走 keyRaw，因为明文那份是转义后的展示名，语义已经变了。
func DecodeKey(keyRaw, plain string) (string, error) {
	if keyRaw != "" {
		b, err := base64.StdEncoding.DecodeString(keyRaw)
		if err != nil {
			return "", fmt.Errorf("键名编码不合法")
		}
		return string(b), nil
	}
	return strings.TrimSpace(plain), nil
}
