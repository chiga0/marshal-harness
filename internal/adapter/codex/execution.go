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
	"sort"
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

const (
	codexLauncherArgument = "__marshal_codex_fchdir_exec_v1"
	launcherWorktreeFD    = 5 // stdin/stdout/stderr + schema/result ExtraFiles
	launcherCloseFailure  = "inject-close-failure"
)

var (
	// errSecureFDExecutionUnavailable is returned only by the platform layer.
	// Callers expose its stable reason through Probe/doctor instead of silently
	// falling back to a pathname-based exec.
	errSecureFDExecutionUnavailable = errors.New("authenticated fd execution is unavailable")
)

// init implements the minimal controlled launcher used by Run. The already
// running Marshal executable re-execs itself, fchdir(2)s to the inherited
// worktree descriptor, closes that descriptor, then atomically execs the
// private Codex snapshot. No pathname participates in selecting the cwd.
func init() {
	if len(os.Args) >= 5 && os.Args[1] == codexLauncherArgument {
		if gate := os.Args[3]; gate != "" {
			if err := runLauncherTestGate(gate); err != nil {
				os.Exit(126)
			}
		}
		if err := unix.Fchdir(launcherWorktreeFD); err != nil {
			os.Exit(126)
		}
		if os.Args[4] == launcherCloseFailure {
			_ = unix.Close(launcherWorktreeFD)
		}
		if err := unix.Close(launcherWorktreeFD); err != nil {
			os.Exit(126)
		}
		executable := os.Args[2]
		if strings.HasPrefix(executable, "/proc/self/fd/") {
			// The authenticated launcher image is no longer needed after this
			// point. Do not expose its descriptor to the Worker.
			if err := unix.Close(6); err != nil {
				os.Exit(126)
			}
			// fd 7 is required only while the kernel resolves
			// /proc/self/fd/7 for this exec. Mark it close-on-exec before the
			// transition so the attested Codex image, and therefore every
			// descendant it may spawn, cannot retain the executable authority.
			if _, err := unix.FcntlInt(uintptr(7), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
				os.Exit(126)
			}
		}
		argv := append([]string{executable}, os.Args[5:]...)
		if err := unix.Exec(executable, argv, os.Environ()); err != nil {
			os.Exit(126)
		}
	}
}

// runLauncherTestGate is reachable only when a test-only Adapter field is
// populated. Production construction always passes an empty gate.
func runLauncherTestGate(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("invalid launcher test gate")
	}
	if err := os.WriteFile(filepath.Join(directory, "ready"), nil, 0o600); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "release")); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("launcher test gate timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

type executableSnapshot struct {
	identity executableIdentity
	path     string
	dir      string
	source   *os.File
	file     *os.File
}

// pinnedDirectory keeps a directory inode open for the whole attempt.  All
// security-sensitive descendants are opened relative to this descriptor, so
// renaming or replacing the configured pathname cannot redirect an attempt.
type pinnedDirectory struct {
	path string
	file *os.File
}

// pinnedDescendantDirectory keeps every component of a descendant path open.
// verifyLinked checks each parent/name edge, rather than only the final inode,
// so rename-and-replace of an evidence directory cannot detach durable output
// from the authoritative control-root namespace unnoticed.
type pinnedDescendantDirectory struct {
	files []*os.File
	names []string
}

func (directory *pinnedDescendantDirectory) leaf() *os.File {
	return directory.files[len(directory.files)-1]
}

func (directory *pinnedDescendantDirectory) verifyLinked() error {
	if directory == nil || len(directory.files) != len(directory.names)+1 {
		return errors.New("pinned descendant directory is invalid")
	}
	for index, name := range directory.names {
		parent, child := directory.files[index], directory.files[index+1]
		var opened, linked unix.Stat_t
		if err := unix.Fstat(int(child.Fd()), &opened); err != nil {
			return err
		}
		if err := unix.Fstatat(int(parent.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			linked.Mode&unix.S_IFMT != unix.S_IFDIR || opened.Ino != linked.Ino || opened.Dev != linked.Dev {
			return errors.New("pinned descendant directory pathname was replaced")
		}
	}
	return nil
}

func (directory *pinnedDescendantDirectory) close() {
	if directory == nil {
		return
	}
	for _, file := range directory.files {
		if file != nil {
			_ = file.Close()
		}
	}
	directory.files = nil
}

func openPinnedDirectory(configured string) (*pinnedDirectory, error) {
	if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
		return nil, errors.New("directory path must be absolute and clean")
	}
	real, err := filepath.EvalSymlinks(configured)
	if err != nil || real != configured {
		return nil, errors.New("directory path must not contain symlinks")
	}
	fd, err := unix.Open(real, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &pinnedDirectory{path: real, file: os.NewFile(uintptr(fd), real)}, nil
}

func (directory *pinnedDirectory) close() {
	if directory != nil && directory.file != nil {
		_ = directory.file.Close()
		directory.file = nil
	}
}

func (directory *pinnedDirectory) verifyLinked() error {
	if directory == nil || directory.file == nil {
		return errors.New("pinned directory is closed")
	}
	opened, err := directory.file.Stat()
	if err != nil {
		return err
	}
	linked, err := os.Lstat(directory.path)
	if err != nil || !linked.IsDir() || !os.SameFile(opened, linked) {
		return errors.New("pinned directory pathname was replaced")
	}
	return nil
}

// snapshotExecutable 先从 O_NOFOLLOW 打开的源 inode 复制出私有只读可执行
// 快照，再只对该快照计算 digest、执行 --version 与正式 exec。配置路径在
// 任意两个阶段之间被替换，都不会改变本 attempt 实际执行的字节。
func snapshotExecutable(ctx context.Context, configured string, hook func(string), unsafePathTest bool) (*executableSnapshot, error) {
	if !unsafePathTest {
		return snapshotExecutableByFD(ctx, configured, hook)
	}
	return snapshotExecutableByPathForTest(ctx, configured, hook)
}

// snapshotExecutableByFD binds digest, version probe and the later worker exec
// to one persistently-held inode. No pathname snapshot is created.
func snapshotExecutableByFD(ctx context.Context, configured string, hook func(string)) (*executableSnapshot, error) {
	source, err := openExecutableSourceFD(configured)
	if err != nil {
		return nil, err
	}
	file, err := sealExecutableSourceFD(source)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = file.Close()
			_ = source.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("configured codex executable is unavailable")
	}
	digest, err := digestOpenFile(file)
	if err != nil {
		return nil, err
	}
	if hook != nil {
		hook("after-executable-digest")
	}
	version, err := readBinaryVersionFromFD(ctx, file)
	if err != nil {
		return nil, err
	}
	failed = false
	return &executableSnapshot{
		identity: executableIdentity{path: configured, digest: digest, version: version},
		source:   source,
		file:     file,
	}, nil
}

// snapshotExecutableByPathForTest preserves deterministic script fixtures on
// platforms without fd-exec. Production construction can never select it.
func snapshotExecutableByPathForTest(ctx context.Context, configured string, hook func(string)) (*executableSnapshot, error) {
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
	if s == nil {
		return
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if s.source != nil {
		_ = s.source.Close()
		s.source = nil
	}
	if s.dir != "" {
		_ = os.Chmod(s.dir, 0o700)
		_ = os.RemoveAll(s.dir)
		s.dir = ""
	}
}

// buildArgs 生成冻结的 captured-mode argv。全局参数必须位于 exec 子命令
// 之前：approval=never、-c 显式关闭 workspace-write 网络；worktree 由
// 受控 launcher 对继承 fd 执行 fchdir，不把可变路径作为 -C 交给 CLI。
// --color 是 0.145.0 的 exec 子命令参数，必须放在
// exec 之后。exec 子命令还携带 JSONL 输出、ephemeral、
// 忽略用户配置与 rules、workspace-write sandbox 枚举，以及经 containment/
// symlink 检查后持续持有的 Schema/result fd 路径。--output-last-message
// 机械指向受控 WorkerResult fd，使同一 inode 成为唯一真实结果来源：Run
// 从持有的 fd 读取并验证，杜绝路径替换与未经 argv 绑定的旁路结果。prompt
// 经 stdin 传入而非 argv；model 仅在 TaskSpec 声明非空时追加。所有 argv
// 直接构造，不经 shell。
func buildArgs(schemaPath, resultPath, model string) []string {
	args := []string{
		"--ask-for-approval", "never",
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
	dir                        *pinnedDescendantDirectory
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

// providerSchemaDocument projects the durable WorkerResult schema to the
// Codex provider-facing subset. Codex 0.145.0 rejects the `not` keyword;
// durable validation still uses the original schema after execution.
func providerSchemaDocument(schemaDocument []byte) ([]byte, error) {
	doc, err := decodeStrictJSON(schemaDocument)
	if err != nil {
		return nil, err
	}
	if root, ok := doc.(map[string]any); ok {
		if definitions, ok := root["$defs"].(map[string]any); ok {
			delete(root, "$defs")
			doc = expandProviderSchemaRefs(root, definitions)
		}
	}
	stripRejectedSchemaKeywords(doc)
	return json.Marshal(doc)
}

func expandProviderSchemaRefs(node any, definitions map[string]any) any {
	switch value := node.(type) {
	case map[string]any:
		if reference, ok := value["$ref"].(string); ok && strings.HasPrefix(reference, "#/$defs/") {
			if target, found := definitions[strings.TrimPrefix(reference, "#/$defs/")]; found {
				return expandProviderSchemaRefs(target, definitions)
			}
		}
		for name, child := range value {
			value[name] = expandProviderSchemaRefs(child, definitions)
		}
	case []any:
		for index, child := range value {
			value[index] = expandProviderSchemaRefs(child, definitions)
		}
	}
	return node
}

func stripRejectedSchemaKeywords(node any) {
	switch value := node.(type) {
	case map[string]any:
		delete(value, "$schema")
		delete(value, "$id")
		delete(value, "title")
		delete(value, "not")
		delete(value, "oneOf")
		delete(value, "format")
		delete(value, "pattern")
		delete(value, "uniqueItems")
		delete(value, "minLength")
		delete(value, "maxLength")
		if constant, ok := value["const"]; ok {
			value["type"] = providerSchemaType(constant)
			value["enum"] = []any{constant}
			delete(value, "const")
		}
		if properties, ok := value["properties"].(map[string]any); ok {
			required := make([]string, 0, len(properties))
			for name := range properties {
				required = append(required, name)
			}
			sort.Strings(required)
			value["required"] = required
		}
		for _, child := range value {
			stripRejectedSchemaKeywords(child)
		}
	case []any:
		for _, child := range value {
			stripRejectedSchemaKeywords(child)
		}
	}
}

func providerSchemaType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case nil:
		return "null"
	default:
		return "string"
	}
}

type providerSchemaCompatibilityError struct {
	reasonCode string
}

func (e *providerSchemaCompatibilityError) Error() string { return e.reasonCode }

// prepareAttemptEvidence 打开并钉住 evidence directory inode，然后只经
// openat(O_EXCL|O_NOFOLLOW) 占用全部 attempt 叶子。后续 worker I/O 与
// Adapter 落盘都使用这些持续打开的 fd，不再按可被替换的路径重新打开。
func prepareAttemptEvidence(dir *pinnedDescendantDirectory, resultName string, schemaDocument []byte, mutateForTest func([]byte) []byte) (*attemptEvidence, error) {
	evidence := &attemptEvidence{
		dir: dir, resultName: resultName, schemaName: "codex-output-schema.json",
		transcriptName: "codex-transcript.jsonl", stderrName: "codex-stderr.log",
		metadataName: "codex-transcript-meta.json",
	}
	providerSchema, err := providerSchemaDocument(schemaDocument)
	if err != nil {
		return nil, fmt.Errorf("project codex output schema: %w", err)
	}
	if mutateForTest != nil {
		providerSchema = mutateForTest(append([]byte(nil), providerSchema...))
	}
	profileDocument, err := frozenProviderSchemaProfileDocument()
	if err != nil {
		return nil, &providerSchemaCompatibilityError{reasonCode: providerProfileInvalid}
	}
	compatibility, err := CheckProviderSchemaCompatibility(providerSchema, profileDocument)
	if err != nil {
		var checkErr *ProviderSchemaCheckError
		if errors.As(err, &checkErr) {
			return nil, &providerSchemaCompatibilityError{reasonCode: checkErr.ReasonCode}
		}
		return nil, &providerSchemaCompatibilityError{reasonCode: providerSchemaJSONInvalid}
	}
	if compatibility.Status != "pass" || compatibility.ReasonCode != providerSchemaCompatible || compatibility.IssueCount != 0 || len(compatibility.Issues) != 0 {
		return nil, &providerSchemaCompatibilityError{reasonCode: providerSchemaIncompatible}
	}
	failed := true
	defer func() {
		if failed {
			evidence.close()
		}
	}()
	claims := []struct {
		name, kind string
		content    []byte
		readOnly   bool
		target     **os.File
	}{
		{evidence.schemaName, "output schema", providerSchema, true, &evidence.schema},
		{evidence.resultName, "result", nil, false, &evidence.result},
		{evidence.transcriptName, "transcript", nil, false, &evidence.transcript},
		{evidence.stderrName, "stderr", nil, false, &evidence.stderr},
		{evidence.metadataName, "metadata", nil, false, &evidence.metadata},
	}
	for _, claim := range claims {
		file, claimErr := claimLeafAt(evidence.dir.leaf(), claim.name, claim.content, claim.kind)
		if claimErr != nil {
			return nil, claimErr
		}
		if claim.readOnly {
			readFD, openErr := unix.Openat(int(evidence.dir.leaf().Fd()), claim.name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
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
	if err := evidence.dir.leaf().Sync(); err != nil {
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

func (e *attemptEvidence) verifyLeaves() error {
	if err := e.dir.verifyLinked(); err != nil {
		return err
	}
	for _, leaf := range []struct {
		name string
		file *os.File
	}{{e.resultName, e.result}, {e.schemaName, e.schema}, {e.transcriptName, e.transcript}, {e.stderrName, e.stderr}, {e.metadataName, e.metadata}} {
		var opened unix.Stat_t
		if err := unix.Fstat(int(leaf.file.Fd()), &opened); err != nil {
			return err
		}
		var linked unix.Stat_t
		err := unix.Fstatat(int(e.dir.leaf().Fd()), leaf.name, &linked, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil || linked.Mode&unix.S_IFMT != unix.S_IFREG || opened.Ino != linked.Ino || opened.Dev != linked.Dev {
			return errors.New("attempt evidence leaf was replaced")
		}
	}
	return nil
}

func (e *attemptEvidence) close() {
	for _, file := range []*os.File{e.result, e.schema, e.transcript, e.stderr, e.metadata} {
		if file != nil {
			_ = file.Close()
		}
	}
	if e.dir != nil {
		e.dir.close()
	}
}

func inheritedFilePath(index int) string { return fmt.Sprintf("/dev/fd/%d", 3+index) }

// prepareEvidenceDirectory traverses/creates the result parent strictly from
// the pinned control-root descriptor.  It never resolves a descendant through
// a mutable absolute pathname.
func prepareEvidenceDirectory(root *os.File, resultRelative string) (*pinnedDescendantDirectory, string, error) {
	if resultRelative == "" || filepath.IsAbs(resultRelative) || filepath.Clean(resultRelative) != resultRelative {
		return nil, "", errors.New("attempt result path is not a clean relative path")
	}
	parts := strings.Split(resultRelative, string(filepath.Separator))
	if len(parts) < 2 {
		return nil, "", errors.New("attempt result path must include an evidence directory")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, "", errors.New("attempt result path contains an unsafe component")
		}
	}
	rootFD, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, "", err
	}
	directory := &pinnedDescendantDirectory{files: []*os.File{os.NewFile(uintptr(rootFD), "control-root")}}
	current := directory.files[0]
	for _, part := range parts[:len(parts)-1] {
		fd, openErr := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), part, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				directory.close()
				return nil, "", mkdirErr
			}
			fd, openErr = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			directory.close()
			return nil, "", openErr
		}
		next := os.NewFile(uintptr(fd), part)
		directory.names = append(directory.names, part)
		directory.files = append(directory.files, next)
		current = next
	}
	if err := directory.verifyLinked(); err != nil {
		directory.close()
		return nil, "", err
	}
	return directory, parts[len(parts)-1], nil
}

// workerEnvironment 是 worker 进程环境的完整替换（而非 os.Environ 追加）：
// 仅从固定基础集合按需传递 HOME、CODEX_HOME、PATH、LANG/LC_*、USER/LOGNAME、
// SHELL/TERM、TMP/TEMP/TMPDIR 与 XDG cache/data/state 位置，并追加固定隔离
// 变量。PWD 不透传或重建，避免 CLI 在 Start 后重新解析可变 worktree 路径。
// OPENAI_API_KEY、CODEX_API_KEY、GITHUB_TOKEN、GH_TOKEN、AWS_*、
// GOOGLE_*、ANTHROPIC_API_KEY、DASHSCOPE_API_KEY、代理变量、SSH_AUTH_SOCK、
// MARSHAL_* 及任意未列变量一律不继承。CODEX_HOME 仅透传给 Codex 自身定位
// 既有认证，Adapter 从不读取、复制、打印或写入其中内容。
func workerEnvironment() []string {
	allowed := map[string]bool{"HOME": true, "CODEX_HOME": true, "PATH": true, "LANG": true, "USER": true, "LOGNAME": true, "SHELL": true, "TERM": true, "TMP": true, "TEMP": true, "TMPDIR": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	environment := make([]string, 0, len(allowed)+6)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (allowed[key] || strings.HasPrefix(key, "LC_")) {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
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

func digestConfiguredExecutable(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("configured codex executable is unavailable")
	}
	return digestOpenFile(file)
}

func digestOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

type taskProjection struct {
	model string
	tools []string
}

// readTaskProjection 对冻结 TaskSpec 只做一次受限读取；model 与 tools 从
// 同一字节快照投影，任何路径、读取或 JSON/工具声明解析失败均 fail closed。
func readTaskProjection(controlRoot *os.File, relative, expectedDigest string, validator *contract.Validator) (taskProjection, error) {
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
func readInputFileAt(controlRoot *os.File, relative string, limit int64) ([]byte, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, errors.New("input path is not a clean relative path")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("input path contains an unsafe component")
		}
	}
	rootFD, err := unix.Dup(int(controlRoot.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(rootFD), "control-root")
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
