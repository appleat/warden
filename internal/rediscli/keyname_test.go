package rediscli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKeyDisplay(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		want   string
		escape bool
	}{
		{"普通键", "user:1", "user:1", false},
		{"含中文", "用户:1", "用户:1", false},
		{"含空格", "user 1", "user 1", false},
		// Java 序列化的典型前缀：AC ED 00 05 t 00 04
		{"Java 序列化", "\xac\xed\x00\x05t\x00\x04name", `\xac\xed\x00\x05t\x00\x04name`, true},
		{"NUL 字节", "a\x00b", `a\x00b`, true},
		{"换行", "a\nb", `a\x0ab`, true},
		{"制表符", "a\tb", `a\x09b`, true},
		{"DEL", "a\x7fb", `a\x7fb`, true},
		// 反斜杠本身必须转义，否则区分不了「真 \x41」和转义出来的
		{"反斜杠", `a\x41b`, `a\\x41b`, true},
		// 合法 UTF-8 的中文夹在二进制字节里，中文部分保持可读
		{"中文夹二进制", "\xac\xed中文", `\xac\xed中文`, true},
		{"空键", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := KeyDisplay(c.key); got != c.want {
				t.Fatalf("KeyDisplay(%q) = %q，期望 %q", c.key, got, c.want)
			}
			if got := KeyRaw(c.key) != ""; got != c.escape {
				t.Fatalf("KeyRaw(%q) 带值=%v，期望 %v", c.key, got, c.escape)
			}
		})
	}
}

// 转义后的展示名再 base64 解回来，必须字节级一致。
func TestKeyRawRoundTrip(t *testing.T) {
	keys := []string{
		"user:1",
		"\xac\xed\x00\x05t\x00\x04name",
		"\xac\xed\x00\x05sr\x00\x11java.lang.Integer",
		"a\x00\x01\x02\xff",
		`a\x41b`,
		"用户:\x00",
	}
	for _, k := range keys {
		back, err := DecodeKey(KeyRaw(k), KeyDisplay(k))
		if err != nil {
			t.Fatalf("%q 解码失败：%v", k, err)
		}
		if back != k {
			t.Fatalf("往返后不一致：原 %q，回 %q", k, back)
		}
	}
}

func TestDecodeKey(t *testing.T) {
	if k, err := DecodeKey("", "  user:1  "); err != nil || k != "user:1" {
		t.Fatalf("明文键应去空白，得到 %q / %v", k, err)
	}
	if _, err := DecodeKey("!!!", "user:1"); err == nil {
		t.Fatal("非法 base64 应报错")
	}
	// keyRaw 优先：即使同时给了明文，也按原始字节走
	if k, _ := DecodeKey(KeyRaw("\xac\xed\x00\x05t"), "乱码"); k != "\xac\xed\x00\x05t" {
		t.Fatalf("keyRaw 应优先，得到 %q", k)
	}
}

// 关键回归：KeyItem 经 JSON 往返后，二进制键既不能出现替换字符，
// 也必须能用 key_raw 无损还原（这就是页面上乱码 + 键不存在的根因）。
func TestKeyItemJSONRoundTrip(t *testing.T) {
	raw := "\xac\xed\x00\x05t\x00\x04name"
	item := KeyItem{Key: KeyDisplay(raw), KeyRaw: KeyRaw(raw), Type: "string", TTL: -1, Size: 32}

	buf, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if strings.ContainsRune(string(buf), '\uFFFD') {
		t.Fatalf("JSON 里出现了替换字符：%s", buf)
	}

	var back KeyItem
	if err := json.Unmarshal(buf, &back); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	key, err := DecodeKey(back.KeyRaw, back.Key)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if key != raw {
		t.Fatalf("原始键名丢失：期望 %q，实际 %q", raw, key)
	}

	// 普通键不产生 key_raw，JSON 里也就没有这个字段
	buf, _ = json.Marshal(KeyItem{Key: KeyDisplay("user:1"), KeyRaw: KeyRaw("user:1")})
	if strings.Contains(string(buf), "key_raw") {
		t.Fatalf("普通键不该带 key_raw：%s", buf)
	}
}
