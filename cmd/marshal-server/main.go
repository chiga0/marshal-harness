// Command marshal-server runs Marshal's resident Control Plane Public API
// (ADR 0018 §1/§3): the versioned HTTP/JSON endpoints Task create/get/cancel
// and Run approval/status, plus the provider-registration/control Port's
// remote registration endpoints. Core remains the only business authority:
// the server assembles the existing internal packages and never opens a
// second write path.
//
// Transport policy (ADR 0018 §12, enabled with remote registration):
// loopback listeners keep their existing plaintext behavior unchanged when
// no TLS baseline is configured; any non-loopback listener demands the
// complete TLS baseline — server certificate + key and client CA for
// mutual identity validation — at construction time and is additionally
// wrapped with request-signature and nonce/time-window replay protection.
// The remote listener is a peeking listener: only connections whose first
// byte is a TLS handshake record (0x16) enter the handshake; plaintext
// connections are closed at the first byte directly, without any response
// bytes. A loopback listener may explicitly opt into the identical
// TLS-only surface with the complete baseline. A partial baseline refuses
// to start fail closed on every listen address, and a non-loopback bind
// without the complete baseline never starts.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/server"
)

// Process exit codes are stable CLI contract, mirroring the embedded CLI.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// defaultListen freezes the resident server's default loopback address.
const defaultListen = "127.0.0.1:7718"

// shutdownTimeout bounds the graceful shutdown after the stop signal.
const shutdownTimeout = 10 * time.Second

const (
	marshalIdentityTimeout = 10 * time.Second
	marshalIdentityMaxJSON = 64 << 10
	marshalExecutableMax   = 256 << 20
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one marshal-server invocation: it binds the repository
// identity, assembles the Public API and the provider-registration/control
// Port over the existing internal packages and serves them until the
// context is cancelled — loopback listeners keep their frozen plaintext
// behavior unless the complete TLS baseline opts them into the TLS-only
// surface, non-loopback listeners start only with the complete TLS
// baseline plus replay protection and close plaintext connections at the
// first byte (peeking listener, ADR 0018 §12).
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marshal-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", defaultListen, "监听地址：loopback 必须是显式 loopback IP；非 loopback 必须同时提供完整 TLS 基线")
	dir := flags.String("dir", ".", "仓库目录")
	tlsCert := flags.String("tls-cert", "", "TLS 服务端证书（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用，与 --tls-key、--tls-client-ca 缺一不可")
	tlsKey := flags.String("tls-key", "", "TLS 服务端私钥（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用")
	tlsClientCA := flags.String("tls-client-ca", "", "双向身份校验的客户端 CA（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用")
	trustRoots := flags.String("trust-roots", "", "注册信任根 key id 列表（逗号分隔）；为空时注册请求一律 fail closed")
	marshalExecutable := flags.String("marshal-executable", "", "固定 marshal CLI 可执行文件；缺省使用仓库 bin/marshal，Run start 始终复用其 production task-run composition")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "用法：marshal-server [--listen HOST:PORT] [--dir PATH] [--marshal-executable PATH] [--tls-cert PEM --tls-key PEM --tls-client-ca PEM] [--trust-roots ID,...]")
		return exitUsage
	}
	loopback, err := server.ClassifyListen(*listen)
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: %v\n", err)
		return exitUsage
	}
	baseline := server.TLSBaseline{CertFile: *tlsCert, KeyFile: *tlsKey, ClientCAFile: *tlsClientCA}
	if !baseline.Complete() && baseline.HasParts() {
		fmt.Fprintln(stderr, "marshal-server: TLS 基线不完整：--tls-cert、--tls-key、--tls-client-ca 必须同时提供，缺一即拒绝启动；loopback 显式启用 TLS 时同样要求完整基线，loopback 默认路径保持明文不变。")
		return exitUsage
	}
	if !loopback && !baseline.Complete() {
		fmt.Fprintln(stderr, "marshal-server: 非 loopback 监听缺少完整 TLS 配置（--tls-cert、--tls-key、--tls-client-ca）：一切非 loopback 传输首次启用即要求 TLS + 双向身份校验 + replay protection（ADR 0018 §12），拒绝启动；loopback 路径不受影响。")
		return exitUsage
	}
	location, err := repository.Discover(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 定位仓库失败：%v\n", err)
		return exitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "marshal-server: 仓库身份无效：%v（请先执行 marshal init）\n", err)
		return exitFailure
	}
	childEnvironment := marshalChildEnvironment(os.Environ())
	executable, err := bindMarshalExecutable(location.RepositoryRoot, *marshalExecutable, childEnvironment)
	if err != nil {
		fmt.Fprintln(stderr, "marshal-server: 固定 marshal CLI 不可用；请构建 bin/marshal 或显式传入 --marshal-executable。")
		return exitFailure
	}
	apiServer, err := server.New(server.Config{
		StateRoot:      location.StateRoot,
		RepositoryRoot: location.RepositoryRoot,
		RunExecutor: func(ctx context.Context, runID string) error {
			return executeRunThroughFixedCLI(ctx, executable, location.RepositoryRoot, runID, childEnvironment)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 组装 Public API 失败：%v\n", err)
		return exitFailure
	}
	registrationPort, err := server.NewRegistrationPort(server.RegistrationPortConfig{
		StateRoot:       location.StateRoot,
		RepositoryRoot:  location.RepositoryRoot,
		TrustRootKeyIds: splitList(*trustRoots),
	})
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 组装 provider-registration/control Port 失败：%v\n", err)
		return exitFailure
	}
	handler := server.CombineHandlers(apiServer.Handler(), registrationPort)
	transport, err := server.NewTransport(*listen, baseline, handler, server.NewReplayGuard(server.DefaultReplayWindow, nil))
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 组装传输失败：%v\n", err)
		return exitFailure
	}
	listener := transport.Listener
	httpServer := &http.Server{
		Handler:           transport.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- httpServer.Serve(listener) }()
	scheme := "http"
	if transport.TLSEnabled {
		scheme = "https"
	}
	registrationProtocol := server.RegistrationProtocolFamily + "/" + server.RegistrationProtocolVersion
	publicProtocol := server.ProtocolFamily + "/" + server.ProtocolVersion
	// The banner advertises the protocol family owning the enabled surface:
	// a TLS-enabled listener is the remote registration transport, the
	// plaintext loopback listener keeps its frozen public-api banner.
	bannerProtocol := publicProtocol
	if transport.TLSEnabled {
		bannerProtocol = registrationProtocol
	}
	banner := struct {
		Listen               string `json:"listen"`
		Protocol             string `json:"protocol"`
		RegistrationProtocol string `json:"registrationProtocol"`
		PublicProtocol       string `json:"publicProtocol"`
	}{
		Listen:               scheme + "://" + listener.Addr().String(),
		Protocol:             bannerProtocol,
		RegistrationProtocol: registrationProtocol,
		PublicProtocol:       publicProtocol,
	}
	data, err := json.Marshal(banner)
	if err != nil {
		_ = httpServer.Close()
		fmt.Fprintf(stderr, "marshal-server: 编码启动信息失败：%v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "%s\n", data)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return exitOK
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "marshal-server: 服务终止：%v\n", err)
			return exitFailure
		}
		return exitOK
	}
}

// bindMarshalExecutable freezes one canonical executable identity at server
// startup. The child CLI's Mac-first self-identity gate reopens and verifies
// the exact path object, digest, sourceHead and activation on every task run;
// resolving here prevents PATH lookup and random temporary executable use.
type marshalExecutableIdentity struct {
	Path       string
	RawSHA256  string
	Device     uint64
	Inode      uint64
	Size       int64
	Version    string
	SourceHead string
	Profile    string
}

func bindMarshalExecutable(repositoryRoot, configured string, environment []string) (marshalExecutableIdentity, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		path = filepath.Join(repositoryRoot, "bin", "marshal")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(repositoryRoot, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return marshalExecutableIdentity{}, err
	}
	identity, err := observeMarshalExecutable(resolved)
	if err != nil {
		return marshalExecutableIdentity{}, err
	}
	version, err := inspectMarshalVersion(repositoryRoot, resolved, environment)
	if err != nil {
		return marshalExecutableIdentity{}, err
	}
	head, err := repositorySourceHead(repositoryRoot, environment)
	if err != nil || version.Commit != head || version.SelfProfile != selfidentity.LocalProfile ||
		version.OS != "darwin" || strings.TrimSpace(version.Version) == "" {
		return marshalExecutableIdentity{}, errors.New("marshal executable build identity is not the active Mac ordinary-local source")
	}
	after, err := observeMarshalExecutable(resolved)
	if err != nil || !identity.sameObject(after) {
		return marshalExecutableIdentity{}, errors.New("marshal executable changed during startup identity admission")
	}
	identity.Version, identity.SourceHead, identity.Profile = version.Version, version.Commit, version.SelfProfile
	return identity, nil
}

// executeRunThroughFixedCLI delegates to the exact existing production
// composition root. No lifecycle, sandbox, authority or result-ingress logic
// is duplicated in marshal-server. Presentation streams are discarded at the
// trust boundary; durable Run status/events carry the safe diagnostics.
func executeRunThroughFixedCLI(ctx context.Context, executable marshalExecutableIdentity, repositoryRoot, runID string, environment []string) error {
	if err := executable.recheck(repositoryRoot, environment); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable.Path, "task", "run", "--run", runID, "--json")
	command.Dir = repositoryRoot
	command.Env = environment
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = shutdownTimeout
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Signal(os.Interrupt)
	}
	return command.Run()
}

type marshalVersion struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"buildDate"`
	GoVersion   string `json:"goVersion"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	SelfProfile string `json:"selfProfile"`
}

func (identity marshalExecutableIdentity) recheck(repositoryRoot string, environment []string) error {
	observed, err := observeMarshalExecutable(identity.Path)
	if err != nil || !identity.sameObject(observed) {
		return errors.New("marshal executable object identity drifted")
	}
	version, err := inspectMarshalVersion(repositoryRoot, identity.Path, environment)
	if err != nil || version.Version != identity.Version || version.Commit != identity.SourceHead ||
		version.SelfProfile != identity.Profile || version.OS != "darwin" {
		return errors.New("marshal executable build identity drifted")
	}
	head, err := repositorySourceHead(repositoryRoot, environment)
	if err != nil || head != identity.SourceHead {
		return errors.New("repository source head drifted from the fixed marshal build")
	}
	after, err := observeMarshalExecutable(identity.Path)
	if err != nil || !identity.sameObject(after) {
		return errors.New("marshal executable changed during execution admission")
	}
	return nil
}

func (identity marshalExecutableIdentity) sameObject(other marshalExecutableIdentity) bool {
	return identity.Path == other.Path && identity.RawSHA256 == other.RawSHA256 && identity.Device == other.Device &&
		identity.Inode == other.Inode && identity.Size == other.Size
}

func observeMarshalExecutable(path string) (marshalExecutableIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return marshalExecutableIdentity{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() <= 0 || info.Size() > marshalExecutableMax {
		return marshalExecutableIdentity{}, errors.New("marshal executable is not a bounded executable regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return marshalExecutableIdentity{}, errors.New("marshal executable object identity is unavailable")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return marshalExecutableIdentity{}, err
	}
	return marshalExecutableIdentity{
		Path: path, RawSHA256: hex.EncodeToString(hash.Sum(nil)),
		Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: info.Size(),
	}, nil
}

func inspectMarshalVersion(repositoryRoot, executable string, environment []string) (marshalVersion, error) {
	ctx, cancel := context.WithTimeout(context.Background(), marshalIdentityTimeout)
	defer cancel()
	var output boundedBuffer
	output.limit = marshalIdentityMaxJSON
	command := exec.CommandContext(ctx, executable, "version", "--json")
	command.Dir, command.Env, command.Stdin, command.Stdout, command.Stderr = repositoryRoot, environment, strings.NewReader(""), &output, io.Discard
	if err := command.Run(); err != nil {
		return marshalVersion{}, errors.New("marshal version identity probe failed")
	}
	var version marshalVersion
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&version); err != nil {
		return marshalVersion{}, errors.New("marshal version identity is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return marshalVersion{}, errors.New("marshal version identity carries trailing content")
	}
	return version, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if buffer.Len()+len(data) > buffer.limit {
		return 0, errors.New("bounded output exceeded")
	}
	return buffer.Buffer.Write(data)
}

func repositorySourceHead(repositoryRoot string, environment []string) (string, error) {
	// The Mac-first profile binds the repository observation to the platform
	// Git executable instead of trusting a caller-controlled PATH lookup.
	command := exec.Command("/usr/bin/git", "-C", repositoryRoot, "rev-parse", "--verify", "HEAD")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(output))
	if len(head) != 40 {
		return "", errors.New("repository source head is invalid")
	}
	return head, nil
}

func marshalChildEnvironment(environ []string) []string {
	allowed := map[string]bool{
		"HOME": true, "PATH": true, "LANG": true, "USER": true, "LOGNAME": true, "SHELL": true, "TERM": true,
		"TMP": true, "TEMP": true, "TMPDIR": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true,
		"XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "CODEX_HOME": true, "QWEN_HOME": true, "FNM_DIR": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"MARSHAL_LOCAL_DOGFOOD_ACTIVATION": true,
		"MARSHAL_OPENCODE_PATH":            true, "MARSHAL_QWEN_PATH": true, "MARSHAL_QODER_PATH": true,
		"MARSHAL_CODEX_PATH": true, "MARSHAL_PI_PATH": true, "MARSHAL_QODER_MODE": true,
		"MARSHAL_QODER_CONFORMANCE_CONFIG": true, "MARSHAL_CODEX_MODE": true,
		"MARSHAL_CODEX_AUTHORITY_CONFIG": true, "MARSHAL_APAP_ENDPOINT": true,
		"MARSHAL_DARWIN_LAUNCHD_CONFIG": true,
		"MARSHAL_QODER_DISABLE_SEARCH":  true,
	}
	result := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (allowed[name] || strings.HasPrefix(name, "LC_")) {
			result = append(result, entry)
		}
	}
	// marshal-server owns the production execution composition. Parent values
	// cannot select the legacy Worker executor or disable the embedded Sandbox
	// and production gates for the child CLI.
	result = append(result, "MARSHAL_EMBEDDED_SANDBOX=1", "MARSHAL_PRODUCTION_GATE=1")
	return result
}

// splitList splits a comma-separated flag value, trimming blanks.
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
