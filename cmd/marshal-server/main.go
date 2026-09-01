// Command marshal-server runs Marshal's non-production compatibility Public
// API. ADR 0062 removed this independent executable from the trusted binary
// set: it may expose durable query/event projections, but every mutation and
// provider-registration request fails closed. The sole production server
// composition is the fixed `marshal control-plane serve` mode.
//
// Transport policy (ADR 0018 §12):
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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/repository"
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one compatibility marshal-server invocation. It binds the
// repository identity and serves the query/event projection until the context
// is cancelled. DisableMutations is deliberately unconditional: no flag,
// environment variable or alternate executable may promote this binary into
// a production authority root (ADR 0062).
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marshal-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", defaultListen, "监听地址：loopback 必须是显式 loopback IP；非 loopback 必须同时提供完整 TLS 基线")
	dir := flags.String("dir", ".", "仓库目录")
	tlsCert := flags.String("tls-cert", "", "TLS 服务端证书（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用，与 --tls-key、--tls-client-ca 缺一不可")
	tlsKey := flags.String("tls-key", "", "TLS 服务端私钥（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用")
	tlsClientCA := flags.String("tls-client-ca", "", "双向身份校验的客户端 CA（PEM）：非 loopback 监听强制要求；loopback 显式启用 TLS 时使用")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "用法：marshal-server [--listen HOST:PORT] [--dir PATH] [--tls-cert PEM --tls-key PEM --tls-client-ca PEM]")
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
	apiServer, err := server.New(server.Config{
		StateRoot:        location.StateRoot,
		RepositoryRoot:   location.RepositoryRoot,
		DisableMutations: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 组装 Public API 失败：%v\n", err)
		return exitFailure
	}
	transport, err := server.NewTransport(*listen, baseline, apiServer.Handler(), server.NewReplayGuard(server.DefaultReplayWindow, nil))
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
	publicProtocol := server.ProtocolFamily + "/" + server.ProtocolVersion
	banner := struct {
		Listen       string `json:"listen"`
		Protocol     string `json:"protocol"`
		MutationMode string `json:"mutationMode"`
	}{
		Listen:       scheme + "://" + listener.Addr().String(),
		Protocol:     publicProtocol,
		MutationMode: "disabled",
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
