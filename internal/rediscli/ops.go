package rediscli

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrNotExist 表示键不存在（TYPE 返回 none）。
var ErrNotExist = errors.New("键不存在")

// ---------- INFO ----------

// Section 是 INFO 输出中的一段，例如 Server / Memory / Keyspace。
type Section struct {
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

type Field struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Info 保留分段顺序（前端按段渲染），同时提供大小写无关的扁平查找。
type Info struct {
	Sections []Section         `json:"sections"`
	Fields   map[string]string `json:"-"`
}

func (i *Info) Get(key string) string {
	if i == nil {
		return ""
	}
	return i.Fields[strings.ToLower(key)]
}

// GetInt 读取数值型字段，缺失或非法时返回 0。
func (i *Info) GetInt(key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(i.Get(key)), 10, 64)
	return n
}

// GetFloat 读取浮点字段（如 used_cpu_sys）。
func (i *Info) GetFloat(key string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(i.Get(key)), 64)
	return f
}

// ParseInfo 解析 INFO 的原始文本。
// 不同版本、不同模块（甚至中间件兼容层）字段差异很大，所以这里只做
// 「按 # 分段 + key:value 切分」，没有的字段一律留空由调用方兜底。
func ParseInfo(raw string) *Info {
	info := &Info{Fields: map[string]string{}}
	var cur *Section
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			info.Sections = append(info.Sections, Section{Name: name})
			cur = &info.Sections[len(info.Sections)-1]
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		f := Field{Key: strings.TrimSpace(line[:idx]), Value: strings.TrimSpace(line[idx+1:])}
		if cur == nil {
			info.Sections = append(info.Sections, Section{Name: "General"})
			cur = &info.Sections[len(info.Sections)-1]
		}
		cur.Fields = append(cur.Fields, f)
		info.Fields[strings.ToLower(f.Key)] = f.Value
	}
	return info
}

// DBStat 是 Keyspace 段里一行 dbN 的解析结果。
type DBStat struct {
	Index   int   `json:"index"`
	Keys    int64 `json:"keys"`
	Expires int64 `json:"expires"`
	AvgTTL  int64 `json:"avg_ttl"`
}

// Keyspace 解析出所有「有键」的逻辑库，按编号排序。
func (i *Info) Keyspace() []DBStat {
	out := []DBStat{}
	if i == nil {
		return out
	}
	for _, sec := range i.Sections {
		if !strings.EqualFold(sec.Name, "Keyspace") {
			continue
		}
		for _, f := range sec.Fields {
			if !strings.HasPrefix(f.Key, "db") {
				continue
			}
			idx, err := strconv.Atoi(strings.TrimPrefix(f.Key, "db"))
			if err != nil {
				continue
			}
			st := DBStat{Index: idx}
			for _, kv := range strings.Split(f.Value, ",") {
				kv = strings.TrimSpace(kv)
				eq := strings.Index(kv, "=")
				if eq <= 0 {
					continue
				}
				n, _ := strconv.ParseInt(strings.TrimSpace(kv[eq+1:]), 10, 64)
				switch strings.TrimSpace(kv[:eq]) {
				case "keys":
					st.Keys = n
				case "expires":
					st.Expires = n
				case "avg_ttl":
					st.AvgTTL = n
				}
			}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Index < out[b].Index })
	return out
}

// TotalKeys 汇总所有逻辑库的键数量。
func (i *Info) TotalKeys() int64 {
	var n int64
	for _, db := range i.Keyspace() {
		n += db.Keys
	}
	return n
}

// ---------- 键列表 ----------

type KeyItem struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`  // -1 永不过期，-2 已不存在
	Size int64  `json:"size"` // 字节，-1 表示无法获取
}

type KeyPage struct {
	Items    []KeyItem `json:"items"`
	Cursor   string    `json:"cursor"`    // 恒为 "0"：列表已一次性返回全部匹配，不再做游标翻页
	Total    int64     `json:"total"`     // DBSIZE 的结果
	Scanned  int       `json:"scanned"`   // 实际考察过的键数量（用于提示扫描范围）
	Matched  int64     `json:"matched"`   // 实际匹配到的键数（被截断时等于返回上限）
	Truncated bool     `json:"truncated"` // 是否还有更多匹配未被返回
}

// MaxKeysReturn 列表最多返回这么多键，避免超大库把响应撑爆。
const MaxKeysReturn = 5000

// MaxKeysScan 单次扫描最多考察这么多键，防止超大库把请求卡死。
const MaxKeysScan = 500000

// normalizePattern 把用户输入转成 SCAN 的 MATCH 表达式：
//   - 空串视为 *（全部键）
//   - 已包含 glob 元字符（* ? [ ]）时原样使用，让用户精确控制
//   - 否则当作子串，前后补 * 实现直观的「模糊查找」
func normalizePattern(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "*"
	}
	if strings.ContainsAny(p, "*?[]") {
		return p
	}
	return "*" + p + "*"
}

// ListKeys 扫描当前库，返回匹配 pattern 与 typeFilter 的全部键（最多 MaxKeysReturn 条）。
//
// 关键点：SCAN 命中键在键空间里是散落的，第一轮拿到的批次里往往只有零星几个匹配，
// 因此不能「一命中就停」，否则后面的匹配键永远扫不到（这正是之前「有的能查到有的查不到」的根因）。
// 这里一直扫到游标归零，累计所有匹配键后再截断返回。
func ListKeys(ctx context.Context, c *Conn, pattern, typeFilter string, limit int) (*KeyPage, error) {
	pattern = normalizePattern(pattern)
	if limit <= 0 || limit > MaxKeysReturn {
		limit = MaxKeysReturn
	}
	typeFilter = strings.ToLower(strings.TrimSpace(typeFilter))

	page := &KeyPage{Items: []KeyItem{}, Cursor: "0"}
	if total, err := c.Int(ctx, "DBSIZE"); err == nil {
		page.Total = total
	}

	all := make([]KeyItem, 0, limit)
	cursor := "0"
	for {
		raw, err := c.Do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", 200)
		if err != nil {
			return page, err
		}
		arr, ok := raw.([]any)
		if !ok || len(arr) < 2 {
			return page, errors.New("SCAN 响应格式异常")
		}
		cursor = ReplyString(arr[0])
		keys := ReplyStrings(arr[1])
		page.Scanned += len(keys)

		if len(keys) > 0 {
			for _, it := range enrichKeys(ctx, c, keys) {
				if typeFilter != "" && typeFilter != "*" && it.Type != typeFilter {
					continue
				}
				all = append(all, it)
				if len(all) >= limit {
					page.Truncated = true
					cursor = "0"
					break
				}
			}
		}
		if cursor == "0" || page.Truncated {
			break
		}
		if page.Scanned >= MaxKeysScan {
			// 超大库且匹配很稀疏：扫了太多键仍没凑满上限，先停住并提示用户缩小范围
			page.Truncated = true
			break
		}
	}
	page.Items = all
	page.Matched = int64(len(all))
	return page, nil
}

// enrichKeys 用一次 Pipeline 补齐每个键的类型、TTL 与内存占用。
func enrichKeys(ctx context.Context, c *Conn, keys []string) []KeyItem {
	cmds := make([][]any, 0, len(keys)*3)
	for _, k := range keys {
		cmds = append(cmds,
			[]any{"TYPE", k},
			[]any{"TTL", k},
			[]any{"MEMORY", "USAGE", k},
		)
	}
	res, err := c.Pipeline(ctx, cmds)
	items := make([]KeyItem, 0, len(keys))
	for i, k := range keys {
		it := KeyItem{Key: k, Type: "unknown", TTL: -1, Size: -1}
		if err == nil {
			if base := i * 3; base+2 < len(res) {
				if s, ok := res[base].(string); ok {
					it.Type = s
				}
				if n, ok := res[base+1].(int64); ok {
					it.TTL = n
				}
				if n, ok := res[base+2].(int64); ok {
					it.Size = n
				}
			}
		}
		items = append(items, it)
	}
	return items
}

// ---------- 键详情 ----------

type Pair struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// KeyDetail 是键的可视化展示数据。Preview 的结构随 Type 变化：
// string → string；hash/zset → []Pair；list/set → []string；stream → []string。
type KeyDetail struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	TTL       int64  `json:"ttl"`
	Size      int64  `json:"size"`
	Length    int64  `json:"length"`
	Encoding  string `json:"encoding"`
	Preview   any    `json:"preview"`
	Truncated bool   `json:"truncated"`
}

const (
	previewLines  = 100
	previewString = 4096
)

// GetKey 读取一个键的元信息与内容预览。
// 所有预览都有上限，避免误点一个百万元素的 hash 直接把面板拖死。
func GetKey(ctx context.Context, c *Conn, key string) (*KeyDetail, error) {
	typ, err := c.String(ctx, "TYPE", key)
	if err != nil {
		return nil, err
	}
	if typ == "" || typ == "none" {
		return nil, ErrNotExist
	}
	d := &KeyDetail{Key: key, Type: typ, TTL: -1, Size: -1}
	if n, err := c.Int(ctx, "TTL", key); err == nil {
		d.TTL = n
	}
	if n, err := c.Int(ctx, "MEMORY", "USAGE", key); err == nil {
		d.Size = n
	}
	if enc, err := c.String(ctx, "OBJECT", "ENCODING", key); err == nil {
		d.Encoding = enc
	}

	switch typ {
	case "string":
		if n, err := c.Int(ctx, "STRLEN", key); err == nil {
			d.Length = n
			d.Truncated = n > previewString
		}
		v, err := c.String(ctx, "GETRANGE", key, 0, previewString-1)
		if err != nil {
			return nil, err
		}
		d.Preview = v

	case "hash":
		if n, err := c.Int(ctx, "HLEN", key); err == nil {
			d.Length = n
		}
		raw, err := c.Do(ctx, "HSCAN", key, 0, "COUNT", previewLines)
		if err != nil {
			return nil, err
		}
		pairs, hasMore := scanPairs(raw)
		d.Preview = pairs
		d.Truncated = d.Length > int64(len(pairs)) || hasMore

	case "list":
		if n, err := c.Int(ctx, "LLEN", key); err == nil {
			d.Length = n
		}
		raw, err := c.Do(ctx, "LRANGE", key, 0, previewLines-1)
		if err != nil {
			return nil, err
		}
		d.Preview = ReplyStrings(raw)
		d.Truncated = d.Length > int64(len(d.Preview.([]string)))

	case "set":
		if n, err := c.Int(ctx, "SCARD", key); err == nil {
			d.Length = n
		}
		raw, err := c.Do(ctx, "SSCAN", key, 0, "COUNT", previewLines)
		if err != nil {
			return nil, err
		}
		// SSCAN 返回的是扁平的成员列表，不像 HSCAN 那样成对出现
		members, _ := scanItems(raw)
		d.Preview = members
		d.Truncated = d.Length > int64(len(members))

	case "zset":
		if n, err := c.Int(ctx, "ZCARD", key); err == nil {
			d.Length = n
		}
		raw, err := c.Do(ctx, "ZRANGE", key, 0, previewLines-1, "WITHSCORES")
		if err != nil {
			return nil, err
		}
		flat := ReplyStrings(raw)
		pairs := make([]Pair, 0, len(flat)/2)
		for i := 0; i+1 < len(flat); i += 2 {
			pairs = append(pairs, Pair{Field: flat[i], Value: flat[i+1]})
		}
		d.Preview = pairs
		d.Truncated = d.Length > int64(len(pairs))

	case "stream":
		if n, err := c.Int(ctx, "XLEN", key); err == nil {
			d.Length = n
		}
		raw, err := c.Do(ctx, "XRANGE", key, "-", "+", "COUNT", 20)
		if err != nil {
			return nil, err
		}
		entries := []string{}
		if arr, ok := raw.([]any); ok {
			for _, e := range arr {
				entries = append(entries, Format(e))
			}
		}
		d.Preview = entries
		d.Truncated = d.Length > int64(len(entries))

	default:
		// 模块类型（如 ReJSON）没有通用读取方式，只返回元信息
		d.Preview = nil
	}
	return d, nil
}

// scanItems 解析 SCAN/SSCAN 的响应：[游标, [成员...]]。
// 第二个参数表示「游标非 0」，即服务端还有更多数据。
func scanItems(raw any) ([]string, bool) {
	arr, ok := raw.([]any)
	if !ok || len(arr) < 2 {
		return []string{}, false
	}
	return ReplyStrings(arr[1]), ReplyString(arr[0]) != "0"
}

// scanPairs 解析 HSCAN 的响应：[游标, [字段, 值, ...]]，元素成对出现。
func scanPairs(raw any) ([]Pair, bool) {
	arr, ok := raw.([]any)
	if !ok || len(arr) < 2 {
		return []Pair{}, false
	}
	cursor := ReplyString(arr[0])
	flat := ReplyStrings(arr[1])
	pairs := make([]Pair, 0, len(flat)/2+1)
	for i := 0; i+1 < len(flat); i += 2 {
		pairs = append(pairs, Pair{Field: flat[i], Value: flat[i+1]})
	}
	return pairs, cursor != "0"
}

// ---------- 写操作 ----------

// SetString 写入字符串键；ttl>0 时同时设置过期秒数。
func SetString(ctx context.Context, c *Conn, key, value string, ttl int) error {
	args := []any{"SET", key, value}
	if ttl > 0 {
		args = append(args, "EX", ttl)
	}
	_, err := c.Do(ctx, args...)
	return err
}

// SetField 给 hash/zset 写一个字段，或给 list/set 追加一个元素。
// 用 HSET/RPUSH/SADD/ZADD 而不是整键覆盖，避免误删已有数据。
func SetField(ctx context.Context, c *Conn, key, typ, field, value string, ttl int) error {
	var args []any
	switch typ {
	case "hash":
		if field == "" {
			return errors.New("Hash 类型需要填写字段名")
		}
		args = []any{"HSET", key, field, value}
	case "list":
		args = []any{"RPUSH", key, value}
	case "set":
		args = []any{"SADD", key, value}
	case "zset":
		if field == "" {
			return errors.New("ZSet 类型需要填写成员名")
		}
		score, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return errors.New("ZSet 的分值必须是数字")
		}
		args = []any{"ZADD", key, score, field}
	default:
		return errors.New("该类型暂不支持在线编辑，请使用命令台")
	}
	if _, err := c.Do(ctx, args...); err != nil {
		return err
	}
	if ttl > 0 {
		_, _ = c.Do(ctx, "EXPIRE", key, ttl)
	}
	return nil
}

// DeleteKey 删除键，返回实际删除的数量。
func DeleteKey(ctx context.Context, c *Conn, key string) (int64, error) {
	return c.Int(ctx, "DEL", key)
}

// ExpireKey 设置键的过期时间；ttl<=0 表示移除过期（永久保留）。
func ExpireKey(ctx context.Context, c *Conn, key string, ttl int) (bool, error) {
	if ttl <= 0 {
		n, err := c.Int(ctx, "PERSIST", key)
		return n == 1, err
	}
	n, err := c.Int(ctx, "EXPIRE", key, ttl)
	return n == 1, err
}

// ---------- 命令台 ----------

// ParseCommandLine 按 redis-cli 的习惯切分命令行，支持单双引号与反斜杠转义。
func ParseCommandLine(line string) ([]any, error) {
	var (
		args    []any
		cur     strings.Builder
		quote   rune
		escaped bool
		started bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			started = true
		case r == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, errors.New("引号没有闭合")
	}
	if escaped {
		return nil, errors.New("命令末尾存在孤立的反斜杠")
	}
	flush()
	if len(args) == 0 {
		return nil, errors.New("请输入命令")
	}
	return args, nil
}
