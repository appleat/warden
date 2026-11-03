package filemgr

import (
	"bytes"
	"testing"
)

func TestDecodeTextUTF8(t *testing.T) {
	src := []byte("中文 UTF-8 文本\n第二行\n")
	got, enc := DecodeText(src)
	if enc != EncUTF8 {
		t.Fatalf("enc = %q, want utf-8", enc)
	}
	if got != string(src) {
		t.Fatalf("got %q, want %q", got, src)
	}
	back, err := EncodeText(got, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, src) {
		t.Fatalf("roundtrip mismatch: %q", back)
	}
}

func TestDecodeTextGBK(t *testing.T) {
	want := "本机终端打开文本文件，中文不该乱码"
	src, err := EncodeText(want, EncGBK)
	if err != nil {
		t.Fatal(err)
	}
	// 真正的 GBK 中文，必然不是合法 UTF-8
	if bytes.Equal(src, []byte(want)) {
		t.Fatal("GBK 编码结果不该等于 UTF-8")
	}
	got, enc := DecodeText(src)
	if enc != EncGBK {
		t.Fatalf("enc = %q, want gbk", enc)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// 保存时按 GBK 写回，字节保持不变（不会被悄悄改成 UTF-8）
	back, err := EncodeText(got, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, src) {
		t.Fatalf("roundtrip mismatch: %q != %q", back, src)
	}
}

func TestDecodeTextUTF8BOM(t *testing.T) {
	src := append([]byte{0xEF, 0xBB, 0xBF}, []byte("带 BOM 的中文")...)
	got, enc := DecodeText(src)
	if enc != EncUTF8BOM {
		t.Fatalf("enc = %q, want utf-8-bom", enc)
	}
	if got != "带 BOM 的中文" {
		t.Fatalf("got %q", got)
	}
	back, err := EncodeText(got, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, src) {
		t.Fatalf("BOM 未被保留: %q", back)
	}
}

func TestDecodeTextUTF16LE(t *testing.T) {
	want := "记事本保存的 UTF-16 文本"
	src, err := EncodeText(want, EncUTF16LE)
	if err != nil {
		t.Fatal(err)
	}
	got, enc := DecodeText(src)
	if enc != EncUTF16LE {
		t.Fatalf("enc = %q, want utf-16le", enc)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	back, err := EncodeText(got, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, src) {
		t.Fatalf("roundtrip mismatch")
	}
}

func TestDecodeTextASCII(t *testing.T) {
	src := []byte("listen 80\nserver_name example.com;\n")
	got, enc := DecodeText(src)
	if enc != EncUTF8 || got != string(src) {
		t.Fatalf("got %q enc %q", got, enc)
	}
}

func TestEncodingLabel(t *testing.T) {
	cases := map[string]string{
		"":          "UTF-8",
		"utf8":      "UTF-8",
		"utf-8-bom": "UTF-8 (BOM)",
		"gbk":       "GBK",
		"gb2312":    "GBK",
		"cp936":     "GBK",
		"utf-16le":  "UTF-16 LE",
		"UTF-16 BE": "UTF-16 BE",
	}
	for in, want := range cases {
		if got := EncodingLabel(in); got != want {
			t.Errorf("EncodingLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
