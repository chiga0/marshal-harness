package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// sandboxNetworkOverride 是冻结的 workspace-write 网络关闭配置覆盖：
// 0.145.0 的 `exec --sandbox` 只接受 read-only/workspace-write/
// danger-full-access 枚举，网络边界因此通过顶层 -c 配置覆盖显式钉死，
// 绝不以 JSON 策略对象或隐式默认值替代。
const sandboxNetworkOverride = "sandbox_workspace_write.network_access=false"

// buildArgs 生成冻结的 captured-mode argv。全局参数必须位于 exec 子命令
// 之前：approval=never、--color never、-C 钉住解析后的 worktree、-c 显式
// 关闭 workspace-write 网络；exec 子命令携带 JSONL 输出、ephemeral、
// 忽略用户配置与 rules、workspace-write sandbox 枚举，以及经 containment/
// symlink 检查的 Schema 临时物与 result 路径。--output-last-message 机械
// 指向最终受控 WorkerResult 叶子，使该叶子成为唯一真实结果来源：Run 从
// 同一路径读取并验证，杜绝任何未经 argv 绑定的旁路结果。prompt 经 stdin
// 传入而非 argv；model 仅在 TaskSpec 声明非空时追加。所有 argv 直接构造，
// 不经 shell。
func buildArgs(worktree, schemaPath, resultPath, model string) []string {
	args := []string{
		"--ask-for-approval", "never",
		"--color", "never",
		"-C", worktree,
		"-c", sandboxNetworkOverride,
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--json",
		"--sandbox", "workspace-write",
		"--output-schema", schemaPath,
		"--output-last-message", resultPath,
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	return args
}

// attemptLeaf 描述一个启动前必须不存在、启动时以 O_EXCL 原子占用的
// control output 叶子节点。
type attemptLeaf struct {
	path string
	kind string
}

// leafMustBeAbsent 在启动前拒绝任何已存在的叶子节点：普通文件、目录或
// symlink（含指向 control root 外的预存 result 链接）都构成 fail closed
// 输入，真实 worker 不得在归一化之前写入被预置的越界目标。
func leafMustBeAbsent(path, kind string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("attempt %s leaf is a symlink and must not pre-exist", kind)
		}
		return fmt.Errorf("attempt %s leaf unexpectedly pre-exists", kind)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect attempt %s leaf: %w", kind, err)
	}
	return nil
}

// claimLeaf 以 O_EXCL|O_NOFOLLOW 原子占用叶子路径并写入内容（nil 表示
// 空文件），随后用 fstat/lstat 的 dev+ino 比较确认落盘节点仍是同一个
// 普通文件：预置 symlink、并发替换或越界重定向都在启动前 fail closed。
// 该占用与紧随其后的进程启动之间只保留最小 TOCTOU 窗口，且窗口内的
// 写入者必须先控制 control output 目录本身。
func claimLeaf(path string, content []byte, kind string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("attempt %s leaf unexpectedly pre-exists", kind)
		}
		return fmt.Errorf("claim attempt %s leaf: %w", kind, err)
	}
	defer file.Close()
	if len(content) > 0 {
		if _, err := file.Write(content); err != nil {
			return fmt.Errorf("write attempt %s leaf: %w", kind, err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync attempt %s leaf: %w", kind, err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat claimed attempt %s leaf: %w", kind, err)
	}
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("re-inspect attempt %s leaf: %w", kind, err)
	}
	if !linkInfo.Mode().IsRegular() || !os.SameFile(fileInfo, linkInfo) {
		return fmt.Errorf("attempt %s leaf was replaced after claim", kind)
	}
	return nil
}

// workerEnvironment 是 worker 进程环境的完整替换（而非 os.Environ 追加）：
// 仅从固定基础集合按需传递 HOME、CODEX_HOME、PATH、LANG/LC_*、USER/LOGNAME、
// SHELL/TERM、TMP/TEMP/TMPDIR 与 XDG cache/data/state 位置，并追加固定隔离
// 变量。OPENAI_API_KEY、CODEX_API_KEY、GITHUB_TOKEN、GH_TOKEN、AWS_*、
// GOOGLE_*、ANTHROPIC_API_KEY、DASHSCOPE_API_KEY、代理变量、SSH_AUTH_SOCK、
// MARSHAL_* 及任意未列变量一律不继承。CODEX_HOME 仅透传给 Codex 自身定位
// 既有认证，Adapter 从不读取、复制、打印或写入其中内容。
func workerEnvironment(worktree string) []string {
	allowed := map[string]bool{"HOME": true, "CODEX_HOME": true, "PATH": true, "LANG": true, "USER": true, "LOGNAME": true, "SHELL": true, "TERM": true, "TMP": true, "TEMP": true, "TMPDIR": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	environment := make([]string, 0, len(allowed)+6)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (allowed[key] || strings.HasPrefix(key, "LC_")) {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "PWD="+worktree)
	return environment
}

// probeEnvironment 使用比 worker 环境更小的固定集合，同样不继承任何
// token 或凭据。
func probeEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "HOME" || key == "TMPDIR" || key == "LANG" {
			result = append(result, entry)
		}
	}
	return result
}

// processFailureError 只使用固定分类与 exit/signal 信息报告失败的 codex
// 进程。provider stderr 作为有界证据文件单独持久化（codex-stderr.log），
// 从不拼接进返回错误，因此 token、秘密或用户内容不可能进入 Events、
// CLI 输出或 Outcome。
func processFailureError(command *exec.Cmd) error {
	exitCode, signal := processOutcome(command)
	if signal != "" {
		return fmt.Errorf("%w: exit=%d signal=%s", ErrProcessFailed, exitCode, signal)
	}
	return fmt.Errorf("%w: exit=%d", ErrProcessFailed, exitCode)
}

func processOutcome(command *exec.Cmd) (int, string) {
	if command.ProcessState == nil {
		return -1, ""
	}
	exitCode := command.ProcessState.ExitCode()
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return exitCode, status.Signal().String()
	}
	return exitCode, ""
}

func contextError(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return ""
}

// terminateGroup 对整个进程组执行 SIGKILL，采用仓库既有等价方案
// （与 pi/qwen/opencode 一致）：超时、取消、stdout 超限、捕获错误或
// 协议不可恢复时经 sync.Once 只执行一次；Run 主流程始终 Wait/reap。
func terminateGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	group, err := syscall.Getpgid(command.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = command.Process.Kill()
	}
}

// evidenceDirectory 在写入任何证据前再次确认冻结 result 路径与其所在
// 证据目录都保持在冻结 control root 内；已存在的目录额外通过 symlink
// 解析，防止准备后重新链接把证据重定向到 control root 之外。
func evidenceDirectory(controlRoot, resultPath string) (string, error) {
	if err := containedWithin(controlRoot, resultPath); err != nil {
		return "", fmt.Errorf("attempt result path: %w", err)
	}
	dir := filepath.Dir(resultPath)
	if err := containedWithin(controlRoot, dir); err != nil {
		return "", fmt.Errorf("attempt evidence directory: %w", err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		if err := containedWithin(controlRoot, real); err != nil {
			return "", fmt.Errorf("attempt evidence directory: %w", err)
		}
	}
	return dir, nil
}

func containedWithin(root, path string) error {
	if root == "" || !filepath.IsAbs(root) || path == "" || !filepath.IsAbs(path) {
		return errors.New("escapes the control root")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("escapes the control root")
	}
	return nil
}

func lexicalPathWithin(root, relative string) (string, error) {
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes control root")
	}
	return path, nil
}

func existingPathWithin(root, relative string) (string, error) {
	path, err := lexicalPathWithin(root, relative)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("symlink escapes control root")
	}
	return real, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds byte limit")
	}
	return data, nil
}

// readModel 从冻结 TaskSpec 投影 worker.model；读取失败视为未声明，
// 绝不接受运行时自述的 model。
func readModel(controlRoot, relative string) string {
	path, err := existingPathWithin(controlRoot, relative)
	if err != nil {
		return ""
	}
	data, err := readBounded(path, maxResultBytes)
	if err != nil {
		return ""
	}
	var task struct {
		Worker struct {
			Model string `json:"model"`
		} `json:"worker"`
	}
	if json.Unmarshal(data, &task) != nil {
		return ""
	}
	return task.Worker.Model
}

// atomicWrite 以临时文件+rename 原子写入 0600 证据文件。
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".codex-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
