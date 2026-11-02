package nacos

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML 配置在发布前的语法校验。
//
// 背景：Nacos 的配置发布接口不校验内容语法，一行缩进错误也能存进去，
// 等服务真正拉取时才炸。面板这边在发布前先解析一遍，把问题挡在保存前。
//
// 只做语法解析（yaml.Decoder），不做反序列化到具体结构：
// 配置内容可以是映射、序列、标量甚至只有注释，这些都属于合法 YAML。

// IsYAMLConfig 判断一条配置是否应当按 YAML 处理。
// 优先看 Nacos 记录的 type；type 为空或 text 时退回看 Data ID 后缀，
// 因为很多配置项在控制台上类型是留空的，只有文件名能说明格式。
func IsYAMLConfig(typ, dataID string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch t {
	case "yaml", "yml":
		return true
	}
	if t != "" && t != "text" {
		return false
	}
	d := strings.ToLower(strings.TrimSpace(dataID))
	return strings.HasSuffix(d, ".yaml") || strings.HasSuffix(d, ".yml")
}

// ValidateYAML 校验 YAML 语法，支持多文档（--- 分隔）。
// 空内容视为合法：允许先把配置建出来再填内容。
//
// 有意只解析到 yaml.Node、不反序列化成 map：
//   - 解析到 Node 不会展开锚点、也不会因为自定义 tag（如 !!str）报错，
//     避免把「语法合法但结构特殊」的配置误判成错误；
//   - 代价是重复键（两个同名 key）不会被发现。重复键在 YAML 规范里语义是
//     「后者覆盖前者」，各解析器表现不一，Nacos 本身也不拒绝，因此不作为错误拦截。
func ValidateYAML(content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	dec := yaml.NewDecoder(strings.NewReader(content))
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("YAML 格式错误：%s", friendlyYAMLError(err))
		}
	}
}

var yamlLineRe = regexp.MustCompile(`\bline (\d+):\s*`)

// friendlyYAMLError 把 yaml.v3 的英文报错整理成一行中文提示。
// yaml.v3 的原始信息形如：
//
//	yaml: line 3: mapping values are not allowed in this context
//	yaml: unmarshal errors:
//	  line 2: mapping key "a" already defined at line 1
//
// 保留原始英文描述（翻译后反而丢失关键信息），只把行号改成中文并压成一行。
func friendlyYAMLError(err error) string {
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:")
	// 多行错误逐条展开，去掉缩进
	parts := strings.Split(msg, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	msg = strings.TrimSpace(strings.Join(parts, "；"))
	msg = strings.Trim(msg, "；")
	if msg == "" {
		msg = "无法解析"
	}
	msg = yamlLineRe.ReplaceAllString(msg, "第 $1 行：")
	return msg
}
