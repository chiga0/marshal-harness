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
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
)

// sandboxNetworkOverride 是冻结的 workspace-write 网络关闭配置覆盖：
// 0.145.0 的 `exec --sandbox` 只接受 read-only/workspace-write/
// danger-full-access 枚举，网络边界因此通过顶层 -c 配置覆盖显式钉死，
// 绝不以 JSON 策略对象或隐式默认值替代。
const sandboxNetworkOverride = "sandbox_workspace_write.network_access=false"

type executableSnapshot struct {
	identity executableIdentity
	path     string
	dir      string
}

// snapshotExecutable 先从 O_NOFOLLOW 打开的源 inode 复制出私有只读可执行
// 快照，再只对该快照计算 digest、执行 --version 与正式 exec。配置路径在
// 任意两个阶段之间被替换，都不会改变本 attempt 实际执行的字节。
func snapshotExecutable(ctx context.Context, configured string, hook func(string)) (*executableSnapshot, error) {
	sourceFD, err := unix.Open(configured, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	source := os.NewFile(uintptr(sourceFD), configured)
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("configured codex executable is unavailable")
	}
	dir, err := os.MkdirTemp("", ".marshal-codex-executable-")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.Chmod(dir, 0o700)
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "codex")
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o700)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return nil, err
	}
	if err := target.Sync(); err != nil {
		target.Close()
		return nil, err
	}
	if err := target.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o500); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		return nil, err
	}
	digest, err := digestFile(path)
	if err != nil {
		return nil, err
	}
	if hook != nil {
		hook("after-executable-digest")
	}
	version, err := readBinaryVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	failed = false
	return &executableSnapshot{
		identity: executableIdentity{path: configured, digest: digest, version: version},
		path:     path,
		dir:      dir,
	}, nil
}

func (s *executableSnapshot) close() {
	if s == nil || s.dir == "" {
		return
	}
	_ = os.Chmod(s.dir, 0o700)
	_ = os.RemoveAll(s.dir)
	s.dir = ""
}

// buildArgs 生成冻结的 captured-mode argv。全局参数必须位于 exec 子命令
// 之前：approval=never、-C 钉住解析后的 worktree、-c 显式关闭
// workspace-write 网络；--color 是 0.145.0 的 exec 子命令参数，必须放在
// exec 之后。exec 子命令还携带 JSONL 输出、ephemeral、
// 忽略用户配置与 rules、workspace-write sandbox 枚举，以及经 containment/
// symlink 检查后持续持有的 Schema/result fd 路径。--output-last-message
// 机械指向受控 WorkerResult fd，使同一 inode 成为唯一真实结果来源：Run
// 从持有的 fd 读取并验证，杜绝路径替换与未经 argv 绑定的旁路结果。prompt
// 经 stdin 传入而非 argv；model 仅在 TaskSpec 声明非空时追加。所有 argv
// 直接构造，不经 shell。
func buildArgs(worktree, schemaPath, resultPath, model string) []string {
	args := []string{
		"--ask-for-approval", "never",
		"-C", worktree,
		"-c", sandboxNetworkOverride,
		"exec",
		"--color", "never",
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

// attemptEvidence 持有启动前以 dirfd/openat 原子占用的全部证据叶子。
type attemptEvidence struct {
	dirPath                    string
	dir                        *os.File
	resultName, schemaName     string
	transcriptName, stderrName string
	metadataName               string
	result, schema             *os.File
	transcript, stderr         *os.File
	metadata                   *os.File
}

type leafClaimError struct {
	kind string
	err  error
}

func (e *leafClaimError) Error() string { return e.err.Error() }
func (e *leafClaimError) Unwrap() error { return e.err }

// prepareAttemptEvidence 打开并钉住 evidence directory inode，然后只经
// openat(O_EXCL|O_NOFOLLOW) 占用全部 attempt 叶子。后续 worker I/O 与
// Adapter 落盘都使用这些持续打开的 fd，不再按可被替换的路径重新打开。
func prepareAttemptEvidence(dirPath, resultPath string, schemaDocument []byte) (*attemptEvidence, error) {
	dirFD, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open attempt evidence directory: %w", err)
	}
	evidence := &attemptEvidence{
		dirPath: dirPath, dir: os.NewFile(uintptr(dirFD), dirPath),
		resultName: filepath.Base(resultPath), schemaName: "codex-output-schema.json",
		transcriptName: "codex-transcript.jsonl", stderrName: "codex-stderr.log",
		metadataName: "codex-transcript-meta.json",
	}
	failed := true
	defer func() {
		if failed {
			evidence.close()
		}
	}()
	if filepath.Join(dirPath, evidence.resultName) != resultPath {
		return nil, errors.New("attempt result leaf is not directly below the evidence directory")
	}
	if err := evidence.verifyDirectory(); err != nil {
		return nil, err
	}
	claims := []struct {
		name, kind string
		content    []byte
		readOnly   bool
		target     **os.File
	}{
		{evidence.schemaName, "output schema", schemaDocument, true, &evidence.schema},
		{evidence.resultName, "result", nil, false, &evidence.result},
		{evidence.transcriptName, "transcript", nil, false, &evidence.transcript},
		{evidence.stderrName, "stderr", nil, false, &evidence.stderr},
		{evidence.metadataName, "metadata", nil, false, &evidence.metadata},
	}
	for _, claim := range claims {
		file, claimErr := claimLeafAt(evidence.dir, claim.name, claim.content, claim.kind)
		if claimErr != nil {
			return nil, claimErr
		}
		if claim.readOnly {
			readFD, openErr := unix.Openat(int(evidence.dir.Fd()), claim.name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
			if openErr != nil {
				file.Close()
				return nil, &leafClaimError{kind: claim.kind, err: fmt.Errorf("reopen attempt %s leaf read-only: %w", claim.kind, openErr)}
			}
			readFile := os.NewFile(uintptr(readFD), claim.name)
			writtenInfo, writtenErr := file.Stat()
			readInfo, readErr := readFile.Stat()
			file.Close()
			if writtenErr != nil || readErr != nil || !os.SameFile(writtenInfo, readInfo) {
				readFile.Close()
				return nil, &leafClaimError{kind: claim.kind, err: fmt.Errorf("attempt %s leaf changed while reopening", claim.kind)}
			}
			file = readFile
		}
		*claim.target = file
	}
	if err := evidence.dir.Sync(); err != nil {
		return nil, fmt.Errorf("sync attempt evidence directory: %w", err)
	}
	failed = false
	return evidence, nil
}

func claimLeafAt(directory *os.File, name string, content []byte, kind string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("attempt %s leaf name is invalid", kind)
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, &leafClaimError{kind: kind, err: fmt.Errorf("attempt %s leaf unexpectedly pre-exists", kind)}
		}
		return nil, &leafClaimError{kind: kind, err: fmt.Errorf("claim attempt %s leaf: %w", kind, err)}
	}
	file := os.NewFile(uintptr(fd), name)
	if err := replaceFileContents(file, content); err != nil {
		file.Close()
		return nil, &leafClaimError{kind: kind, err: fmt.Errorf("initialize attempt %s leaf: %w", kind, err)}
	}
	return file, nil
}

func replaceFileContents(file *os.File, content []byte) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if len(content) > 0 {
		if _, err := file.Write(content); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func readBoundedFile(file *os.File, limit int64) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds byte limit")
	}
	return data, nil
}

func (e *attemptEvidence) verifyDirectory() error {
	opened, err := e.dir.Stat()
	if err != nil {
		return err
	}
	linked, err := os.Lstat(e.dirPath)
	if err != nil || !linked.IsDir() || !os.SameFile(opened, linked) {
		return errors.New("attempt evidence directory was replaced")
	}
	real, err := filepath.EvalSymlinks(e.dirPath)
	if err != nil || real != e.dirPath {
		return errors.New("attempt evidence directory containment changed")
	}
	return nil
}

func (e *attemptEvidence) verifyLeaves() error {
	if err := e.verifyDirectory(); err != nil {
		return err
	}
	for _, leaf := range []struct {
		name string
		file *os.File
	}{{e.resultName, e.result}, {e.schemaName, e.schema}, {e.transcriptName, e.transcript}, {e.stderrName, e.stderr}, {e.metadataName, e.metadata}} {
		opened, err := leaf.file.Stat()
		if err != nil {
			return err
		}
		linked, err := os.Lstat(filepath.Join(e.dirPath, leaf.name))
		if err != nil || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
			return errors.New("attempt evidence leaf was replaced")
		}
	}
	return nil
}

func (e *attemptEvidence) close() {
	for _, file := range []*os.File{e.result, e.schema, e.transcript, e.stderr, e.metadata, e.dir} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func inheritedFilePath(index int) string { return fmt.Sprintf("/dev/fd/%d", 3+index) }

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
func processFailureError(now time.Time) error {
	return newCodexFailure(port.FailureKindProviderTerminal, ErrProcessFailed, "provider process exited unsuccessfully", now)
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

// evidenceDirectory 在写入任何证据前逐组件准备 result 所在目录。它拒绝
// control root 以下任意既存 symlink/非目录节点；缺失后缀逐级以 0700 创建，
// 每一级随后 realpath 校验仍位于 control root 内。不能解析的缺失后缀不再
// 被当作安全成功，从而关闭 link->outside 与 missing-suffix 组合逃逸。
func evidenceDirectory(controlRoot, resultPath string) (string, error) {
	if err := containedWithin(controlRoot, resultPath); err != nil {
		return "", fmt.Errorf("attempt result path: %w", err)
	}
	rootInfo, err := os.Stat(controlRoot)
	if err != nil || !rootInfo.IsDir() {
		return "", errors.New("attempt control root is not an existing directory")
	}
	dir := filepath.Dir(resultPath)
	if err := containedWithin(controlRoot, dir); err != nil {
		return "", fmt.Errorf("attempt evidence directory: %w", err)
	}
	relative, err := filepath.Rel(controlRoot, dir)
	if err != nil {
		return "", fmt.Errorf("attempt evidence directory: %w", err)
	}
	current := controlRoot
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil {
				return "", fmt.Errorf("create attempt evidence directory: %w", mkdirErr)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect attempt evidence directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("attempt evidence directory must not traverse a symlink")
		}
		if !info.IsDir() {
			return "", errors.New("attempt evidence path component is not a directory")
		}
		real, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve attempt evidence directory: %w", resolveErr)
		}
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

type taskProjection struct {
	model string
	tools []string
}

// readTaskProjection 对冻结 TaskSpec 只做一次受限读取；model 与 tools 从
// 同一字节快照投影，任何路径、读取或 JSON/工具声明解析失败均 fail closed。
func readTaskProjection(controlRoot, relative, expectedDigest string, validator *contract.Validator) (taskProjection, error) {
	data, err := readInputFileAt(controlRoot, relative, maxResultBytes)
	if err != nil {
		return taskProjection{}, fmt.Errorf("read TaskSpec: %w", err)
	}
	if err := validator.Validate(domain.KindTask, data); err != nil {
		return taskProjection{}, errors.New("validate TaskSpec: schema rejected")
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil || digest != expectedDigest {
		return taskProjection{}, errors.New("validate TaskSpec: specDigest mismatch")
	}
	var task struct {
		Worker struct {
			Model string `json:"model"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return taskProjection{}, fmt.Errorf("parse TaskSpec: %w", err)
	}
	tools, err := denials.ParseDeclaredWorkerTools(data)
	if err != nil {
		return taskProjection{}, fmt.Errorf("worker tools: %w", err)
	}
	return taskProjection{model: task.Worker.Model, tools: tools}, nil
}

// readInputFileAt 从钉住的 controlRoot dirfd 开始逐组件 openat，拒绝
// symlink 与非目录中间节点，并从最终 regular-file fd 只读取一次。
func readInputFileAt(controlRoot, relative string, limit int64) ([]byte, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, errors.New("input path is not a clean relative path")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("input path contains an unsafe component")
		}
	}
	rootFD, err := unix.Open(controlRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(rootFD), controlRoot)
	defer func() { _ = current.Close() }()
	for _, part := range parts[:len(parts)-1] {
		fd, openErr := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return nil, openErr
		}
		next := os.NewFile(uintptr(fd), part)
		current.Close()
		current = next
	}
	fd, err := unix.Openat(int(current.Fd()), parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), parts[len(parts)-1])
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("input leaf is not a regular file")
	}
	return readBoundedFile(file, limit)
}
