package filemgr

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// 文本编码标识。前端原样回传，保存时据此还原为原始编码，避免「打开 GBK、
// 保存后变 UTF-8」把文件改坏。
const (
	EncUTF8    = "utf-8"
	EncUTF8BOM = "utf-8-bom"
	EncGBK     = "gbk"
	EncUTF16LE = "utf-16le"
	EncUTF16BE = "utf-16be"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// DecodeText 把磁盘上的原始字节转成 UTF-8 文本，并返回检测到的原始编码。
//
// 编辑器只能显示 UTF-8：Windows 下大量文本文件是 GBK（ANSI），直接 string(b)
// 塞进 JSON 时，无效字节会被 encoding/json 替换成 U+FFFD，页面上就是乱码。
// 远程 Linux 主机上的文件基本都是 UTF-8，所以只有本机终端会踩到。
func DecodeText(b []byte) (string, string) {
	switch {
	case bytes.HasPrefix(b, bomUTF8):
		return string(b[len(bomUTF8):]), EncUTF8BOM
	case bytes.HasPrefix(b, bomUTF16LE):
		if s, ok := decodeUTF16(b[len(bomUTF16LE):], unicode.LittleEndian); ok {
			return s, EncUTF16LE
		}
	case bytes.HasPrefix(b, bomUTF16BE):
		if s, ok := decodeUTF16(b[len(bomUTF16BE):], unicode.BigEndian); ok {
			return s, EncUTF16BE
		}
	}
	if utf8.Valid(b) {
		return string(b), EncUTF8
	}
	// 非 UTF-8：中文环境按 GB18030（GBK 超集）解，能覆盖 cp936 的绝大多数文件。
	if s, ok := decodeWith(simplifiedchinese.GB18030.NewDecoder(), b); ok {
		return s, EncGBK
	}
	if s, ok := decodeWith(simplifiedchinese.GBK.NewDecoder(), b); ok {
		return s, EncGBK
	}
	// 兜底：按 Latin-1 逐字节映射，至少不会丢字节、不会出现替换符。
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String(), EncGBK
}

// EncodeText 把编辑器里的 UTF-8 文本按原始编码写回磁盘。
func EncodeText(s, enc string) ([]byte, error) {
	switch normalizeEnc(enc) {
	case "", EncUTF8:
		return []byte(s), nil
	case EncUTF8BOM:
		out := make([]byte, 0, len(s)+len(bomUTF8))
		out = append(out, bomUTF8...)
		return append(out, s...), nil
	case EncGBK:
		out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(s))
		if err != nil {
			return nil, err
		}
		return out, nil
	case EncUTF16LE:
		out, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder(), []byte(s))
		return out, err
	case EncUTF16BE:
		out, _, err := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder(), []byte(s))
		return out, err
	}
	return []byte(s), nil
}

// EncodingLabel 返回给界面展示的编码名称。
func EncodingLabel(enc string) string {
	switch normalizeEnc(enc) {
	case EncUTF8BOM:
		return "UTF-8 (BOM)"
	case EncGBK:
		return "GBK"
	case EncUTF16LE:
		return "UTF-16 LE"
	case EncUTF16BE:
		return "UTF-16 BE"
	default:
		return "UTF-8"
	}
}

func normalizeEnc(enc string) string {
	e := strings.ToLower(strings.TrimSpace(enc))
	switch e {
	case "utf8":
		return EncUTF8
	case "utf8bom", "utf-8-bom", "utf-8 bom":
		return EncUTF8BOM
	case "gb2312", "gb18030", "gb-18030", "cp936", "ansi", "gbk":
		return EncGBK
	case "utf16le", "utf-16 le", "utf-16le":
		return EncUTF16LE
	case "utf16be", "utf-16 be", "utf-16be":
		return EncUTF16BE
	}
	return e
}

func decodeWith(dec *encoding.Decoder, b []byte) (string, bool) {
	out, _, err := transform.Bytes(dec, b)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

func decodeUTF16(b []byte, order unicode.Endianness) (string, bool) {
	if len(b)%2 != 0 {
		return "", false
	}
	out, _, err := transform.Bytes(unicode.UTF16(order, unicode.IgnoreBOM).NewDecoder(), b)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}
