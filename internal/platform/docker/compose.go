package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// compose 相关标签由 docker compose 启动容器时自动写入，
// 据此可以反推出堆栈名称与 compose 文件的位置。
const (
	labelComposeProject     = "com.docker.compose.project"
	labelComposeWorkingDir  = "com.docker.compose.project.working_dir"
	labelComposeConfigFiles = "com.docker.compose.project.config_files"
)

// ComposeStack 是一个 docker-compose 堆栈的聚合信息。
type ComposeStack struct {
	Project     string   `json:"project"`
	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	Containers  int      `json:"containers"`
	Running     int      `json:"running"`
}

// ComposeFileData 是 compose 配置文件的读取结果。
type ComposeFileData struct {
	Project     string   `json:"project"`
	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	Path        string   `json:"path"`
	Content     string   `json:"content"`
}

// Stacks 从容器标签聚合出 compose 堆栈列表。
// 没有堆栈标签的容器不在此列（前端作为「独立容器」单独展示）。
func (s *Service) Stacks(ctx context.Context) ([]ComposeStack, error) {
	list, err := s.List(ctx, true, "")
	if err != nil {
		return nil, err
	}
	m := map[string]*ComposeStack{}
	for _, c := range list {
		if c.Project == "" {
			continue
		}
		st, ok := m[c.Project]
		if !ok {
			st = &ComposeStack{Project: c.Project}
			m[c.Project] = st
		}
		st.Containers++
		if c.State == "running" {
			st.Running++
		}
		// 文件位置取任意一个容器上的标签即可，同堆栈内容一致
		if st.WorkingDir == "" {
			st.WorkingDir = c.Labels[labelComposeWorkingDir]
			st.ConfigFiles = splitComposeFiles(c.Labels[labelComposeConfigFiles])
		}
	}
	out := make([]ComposeStack, 0, len(m))
	for _, st := range m {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

// splitComposeFiles 拆分 config_files 标签（逗号分隔，可能包含主文件与 override 文件）。
func splitComposeFiles(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// stackInfo 从容器标签定位某个 compose 项目的工作目录与配置文件列表。
func (s *Service) stackInfo(ctx context.Context, project string) (string, []string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", nil, errors.New("缺少 compose 项目名")
	}
	list, err := s.List(ctx, true, "")
	if err != nil {
		return "", nil, err
	}
	for _, c := range list {
		if c.Project != project {
			continue
		}
		wd := c.Labels[labelComposeWorkingDir]
		files := splitComposeFiles(c.Labels[labelComposeConfigFiles])
		if wd != "" && len(files) > 0 {
			return wd, files, nil
		}
	}
	return "", nil, fmt.Errorf("未找到 compose 项目 %q 的容器，或容器缺少文件位置标签（可能不是通过 docker compose 启动）", project)
}

// ComposeFile 读取堆栈的主配置文件内容。多个文件时优先选 docker-compose.yml / compose.yaml。
func (s *Service) ComposeFile(ctx context.Context, project string) (*ComposeFileData, error) {
	wd, files, err := s.stackInfo(ctx, project)
	if err != nil {
		return nil, err
	}
	path := files[0]
	for _, f := range files {
		switch strings.ToLower(filepath.Base(f)) {
		case "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml":
			path = f
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 compose 文件失败: %w", err)
	}
	return &ComposeFileData{
		Project:     project,
		WorkingDir:  wd,
		ConfigFiles: files,
		Path:        path,
		Content:     string(content),
	}, nil
}

// SaveComposeFile 把编辑后的内容写回主配置文件。
// 写入路径必须来自堆栈容器上的标签，不接受前端传任意路径，避免变成任意文件写入。
// 覆盖前会把原内容备份为 <file>.bak。
func (s *Service) SaveComposeFile(ctx context.Context, project, content string) (string, error) {
	data, err := s.ComposeFile(ctx, project)
	if err != nil {
		return "", err
	}
	if old, err := os.ReadFile(data.Path); err == nil {
		_ = os.WriteFile(data.Path+".bak", old, 0o644)
	}
	if err := os.WriteFile(data.Path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入 compose 文件失败: %w", err)
	}
	return data.Path, nil
}

// composeUpTimeout 覆盖镜像拉取等慢操作，compose up 的整体上限。
const composeUpTimeout = 5 * time.Minute

// ComposeUp 应用堆栈配置：docker compose up -d。
// compose 不是 Engine API 的能力，需要调用宿主机上的 docker CLI（compose 插件或独立 docker-compose）。
// 项目名、工作目录与配置文件都取自堆栈容器上的标签，保证与堆栈原始启动方式一致。
func (s *Service) ComposeUp(ctx context.Context, project string) (string, error) {
	wd, files, err := s.stackInfo(ctx, project)
	if err != nil {
		return "", err
	}

	var bin string
	var baseArgs []string
	if dockerBin, err := exec.LookPath("docker"); err == nil {
		if probeCompose(dockerBin) {
			bin = dockerBin
			baseArgs = []string{"compose"}
		}
	}
	if bin == "" {
		if legacy, err := exec.LookPath("docker-compose"); err == nil {
			bin = legacy
			baseArgs = []string{}
		} else {
			return "", errors.New("未找到可用的 docker compose（需要 docker CLI 的 compose 插件，或独立的 docker-compose 命令）")
		}
	}

	args := append(baseArgs,
		"--project-name", project,
		"--project-directory", wd,
	)
	for _, f := range files {
		args = append(args, "-f", f)
	}
	args = append(args, "up", "-d", "--remove-orphans")

	cctx, cancel := context.WithTimeout(ctx, composeUpTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = wd
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	combined := strings.TrimSpace(strings.TrimSpace(out.String()) + "\n" + strings.TrimSpace(errb.String()))
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return combined, errors.New("compose up 超时（5 分钟），请检查镜像是否可拉取")
		}
		if combined == "" {
			combined = err.Error()
		}
		return combined, fmt.Errorf("compose up 失败: %s", combined)
	}
	if combined == "" {
		combined = "应用完成"
	}
	return combined, nil
}

// probeCompose 探测 docker CLI 是否带 compose 插件。
func probeCompose(dockerBin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, dockerBin, "compose", "version").Run() == nil
}
