package nacos

import (
	"strings"
	"testing"
)

func TestIsYAMLConfig(t *testing.T) {
	cases := []struct {
		typ, dataID string
		want        bool
	}{
		{"yaml", "a.yaml", true},
		{"YAML", "a.txt", true},
		{"yml", "a.yml", true},
		{" yaml ", "anything", true},
		{"", "application-dev.yaml", true},
		{"", "application-dev.yml", true},
		{"", "application-dev.YAML", true},
		{"text", "app.yaml", true},
		{"text", "app.properties", false},
		{"properties", "app.yaml", false},
		{"json", "app.json", false},
		{"", "app.json", false},
		{"", "app.txt", false},
		{"xml", "a.xml", false},
	}
	for _, c := range cases {
		if got := IsYAMLConfig(c.typ, c.dataID); got != c.want {
			t.Errorf("IsYAMLConfig(%q, %q) = %v, want %v", c.typ, c.dataID, got, c.want)
		}
	}
}

func TestValidateYAMLAccepts(t *testing.T) {
	ok := []string{
		"",
		"   \n\t\n",
		"# 只有注释\n# 什么都不配\n",
		"server:\n  port: 8080\n  name: demo\n",
		"- a\n- b\n",
		"just-a-scalar\n",
		"a: 1\n---\nb: 2\n",
		"list:\n  - name: x\n    port: 1\n  - name: y\n    port: 2\n",
		"quoted: \"含: 冒号\"\nmulti: |\n  line1\n  line2\n",
	}
	for _, s := range ok {
		if err := ValidateYAML(s); err != nil {
			t.Errorf("ValidateYAML(%q) 应为合法，却报错: %v", s, err)
		}
	}
}

func TestValidateYAMLRejects(t *testing.T) {
	cases := []struct {
		name    string
		content string
		line    string // 期望出现的行号片段
	}{
		{"tab 缩进", "server:\n\tport: 8080\n", "第 2 行"},
		{"冒号后多余内容", "a: 1\nb: c: d\n", "第 2 行"},
		{"缩进错乱", "a:\n  b: 1\n c: 2\n", ""},
		{"未闭合的引号", "a: \"unclosed\n", ""},
	}
	for _, c := range cases {
		err := ValidateYAML(c.content)
		if err == nil {
			t.Errorf("%s：应报错，却通过了", c.name)
			continue
		}
		if !strings.HasPrefix(err.Error(), "YAML 格式错误：") {
			t.Errorf("%s：错误信息缺少前缀: %v", c.name, err)
		}
		if c.line != "" && !strings.Contains(err.Error(), c.line) {
			t.Errorf("%s：错误信息应包含 %q，实际: %v", c.name, c.line, err)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("%s：错误信息应为单行，实际: %v", c.name, err)
		}
	}
}
