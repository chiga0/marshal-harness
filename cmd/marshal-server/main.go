// Command marshal-server runs Marshal's resident Control Plane Public API
// (ADR 0018 §1/§3): the versioned HTTP/JSON endpoints Task create/get/cancel
// and Run approval/status, served on a loopback listener. Core remains the
// only business authority: the server assembles the existing internal
// packages and never opens a second write path. Remote registration and
// non-loopback transports belong to later milestones and stay disabled.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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

// run executes one marshal-server invocation: it binds the repository
// identity, assembles the Public API over the existing internal packages and
// serves it on an explicit loopback listener until the context is cancelled.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("marshal-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", defaultListen, "监听地址，主机必须是显式 loopback IP 地址")
	dir := flags.String("dir", ".", "仓库目录")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "用法：marshal-server [--listen HOST:PORT] [--dir PATH]")
		return exitUsage
	}
	if err := validateLoopbackListen(*listen); err != nil {
		fmt.Fprintf(stderr, "marshal-server: %v\n", err)
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
		StateRoot:      location.StateRoot,
		RepositoryRoot: location.RepositoryRoot,
	})
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 组装 Public API 失败：%v\n", err)
		return exitFailure
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "marshal-server: 监听失败：%v\n", err)
		return exitFailure
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		_ = listener.Close()
		fmt.Fprintln(stderr, "marshal-server: 绑定地址不是 loopback，拒绝启动。")
		return exitFailure
	}
	httpServer := &http.Server{
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- httpServer.Serve(listener) }()
	banner := struct {
		Listen   string `json:"listen"`
		Protocol string `json:"protocol"`
	}{
		Listen:   "http://" + listener.Addr().String(),
		Protocol: server.ProtocolFamily + "/" + server.ProtocolVersion,
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

// validateLoopbackListen fails closed on any listen address that is not an
// explicit loopback IP: wildcard hosts, host names and routable addresses
// all stay disabled until the remote transport security baseline is enabled
// by a later milestone (ADR 0018 §12).
func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("监听地址无效：%w", err)
	}
	if host == "" {
		return errors.New("监听主机必须是显式 loopback IP 地址，禁止绑定全部接口")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("监听主机必须是显式 loopback IP 地址，非 loopback 传输要求尚未启用的 TLS 安全基线")
	}
	return nil
}
