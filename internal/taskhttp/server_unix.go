//go:build darwin || linux

package taskhttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"golang.org/x/sys/unix"
)

type ControlAuthority interface {
	Recheck(context.Context) error
	WithControlMutation(context.Context, func(*os.File) error) error
}
type ServerConfig struct {
	Application       application.TaskDraftPort
	Address           string
	RecordName        string
	ControlPath       string
	Authority         ControlAuthority
	Mutation          MutationLane
	AutomaticDecision bool
}
type Server struct {
	HTTP           *http.Server
	listener       net.Listener
	authority      ControlAuthority
	recordName     string
	recordFile     *os.File
	recordDir      *os.File
	recordStat     unix.Stat_t
	Address        string
	ConnectionFile string
}

// OpenServer requires the existing application's current held control root.
// It never reads an existing credential file or publishes a token to stdout.
func OpenServer(ctx context.Context, c ServerConfig) (*Server, error) {
	fail := errors.New("task HTTP: protected loopback setup failed")
	host, _, err := net.SplitHostPort(c.Address)
	if ctx == nil || err != nil || host != "127.0.0.1" || c.Authority == nil || !filepath.IsAbs(c.ControlPath) || filepath.Clean(c.RecordName) != c.RecordName || strings.ContainsAny(c.RecordName, "/\\") || !strings.HasPrefix(c.RecordName, "task-http-") || len(c.RecordName) > 100 {
		return nil, fail
	}
	if c.Authority.Recheck(ctx) != nil {
		return nil, fail
	}
	listener, err := net.Listen("tcp4", c.Address)
	if err != nil {
		return nil, fail
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		_ = listener.Close()
		return nil, fail
	}
	s := &Server{listener: &limitedListener{Listener: listener, slots: make(chan struct{}, 64)}, authority: c.Authority, recordName: c.RecordName, Address: "http://" + listener.Addr().String(), ConnectionFile: filepath.Join(c.ControlPath, c.RecordName)}
	cleanup := func() {
		_ = s.listener.Close()
		if s.recordFile != nil {
			_ = s.recordFile.Close()
		}
		if s.recordDir != nil {
			_ = s.recordDir.Close()
		}
	}
	handler, err := NewHandler(HandlerConfig{Application: c.Application, Host: listener.Addr().String(), Token: hex.EncodeToString(token[:]), Recheck: s.recheck, Mutation: c.Mutation, AutomaticDecision: c.AutomaticDecision})
	if err != nil {
		cleanup()
		return nil, fail
	}
	s.HTTP = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	err = c.Authority.WithControlMutation(ctx, func(dir *os.File) error {
		dup, err := unix.FcntlInt(dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return fail
		}
		s.recordDir = os.NewFile(uintptr(dup), "task-http-control")
		fd, err := unix.Openat(int(dir.Fd()), c.RecordName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return fail
		}
		s.recordFile = os.NewFile(uintptr(fd), c.RecordName)
		data, _ := json.Marshal(map[string]string{"url": s.Address, "token": hex.EncodeToString(token[:]), "profile": "task-draft/v1"})
		if _, err := s.recordFile.Write(data); err != nil {
			return fail
		}
		if s.recordFile.Sync() != nil || unix.Fstat(fd, &s.recordStat) != nil || unix.Fsync(int(dir.Fd())) != nil {
			return fail
		}
		return nil
	})
	if err != nil {
		cleanup()
		return nil, fail
	}
	if s.recheck(ctx) != nil {
		cleanup()
		return nil, fail
	}
	return s, nil
}

func (s *Server) recheck(ctx context.Context) error {
	if s == nil || s.recordFile == nil || s.recordDir == nil || s.authority.Recheck(ctx) != nil {
		return errors.New("task HTTP: owner unavailable")
	}
	var current unix.Stat_t
	if unix.Fstat(int(s.recordFile.Fd()), &current) != nil || current.Dev != s.recordStat.Dev || current.Ino != s.recordStat.Ino || current.Mode != s.recordStat.Mode || current.Nlink != 1 || current.Size != s.recordStat.Size {
		return errors.New("task HTTP: connection identity changed")
	}
	if unix.Fstatat(int(s.recordDir.Fd()), s.recordName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != s.recordStat.Dev || current.Ino != s.recordStat.Ino || current.Mode != s.recordStat.Mode || current.Nlink != 1 {
		return errors.New("task HTTP: connection path changed")
	}
	return nil
}
func (s *Server) Serve() error {
	err := s.HTTP.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
func (s *Server) StopAccept() error {
	err := s.listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown(ctx context.Context) error { return s.HTTP.Shutdown(ctx) }
func (s *Server) CloseRecord(ctx context.Context) error {
	err := s.authority.WithControlMutation(ctx, func(dir *os.File) error {
		var current unix.Stat_t
		if unix.Fstatat(int(dir.Fd()), s.recordName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != s.recordStat.Dev || current.Ino != s.recordStat.Ino || current.Nlink != 1 {
			return errors.New("task HTTP: connection identity changed")
		}
		if err := unix.Unlinkat(int(dir.Fd()), s.recordName, 0); err != nil {
			return err
		}
		return unix.Fsync(int(dir.Fd()))
	})
	return errors.Join(err, s.recordFile.Close(), s.recordDir.Close())
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
