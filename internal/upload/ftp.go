package upload

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// FTPConfig for FTP/FTPS backend.
type FTPConfig struct {
	Host     string `yaml:"host"` // host:port
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Path     string `yaml:"path"` // remote base path
	TLS      bool   `yaml:"tls"`
}

type ftpBackend struct {
	cfg  *FTPConfig
	mu   sync.Mutex
	conn *ftp.ServerConn
}

func NewFTPBackend(cfg *FTPConfig) Backend {
	return &ftpBackend{cfg: cfg}
}

func (b *ftpBackend) connect() (*ftp.ServerConn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn != nil {
		// Test if connection is still alive
		if err := b.conn.NoOp(); err == nil {
			return b.conn, nil
		}
		b.conn.Quit()
		b.conn = nil
	}

	var opts []ftp.DialOption
	opts = append(opts, ftp.DialWithTimeout(10*time.Second))
	if b.cfg.TLS {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{
			InsecureSkipVerify: true,
		}))
	}

	conn, err := ftp.Dial(b.cfg.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("ftp: dial %s: %w", b.cfg.Host, err)
	}

	if err := conn.Login(b.cfg.Username, b.cfg.Password); err != nil {
		conn.Quit()
		return nil, fmt.Errorf("ftp: login: %w", err)
	}

	b.conn = conn
	return conn, nil
}

func (b *ftpBackend) Download(_ context.Context, remotePath, localPath string) error {
	conn, err := b.connect()
	if err != nil {
		return err
	}

	fullPath := path.Join(b.cfg.Path, remotePath)
	resp, err := conn.Retr(fullPath)
	if err != nil {
		b.mu.Lock()
		b.conn = nil
		b.mu.Unlock()
		return fmt.Errorf("ftp: retr %s: %w", fullPath, err)
	}
	defer resp.Close()

	dir := path.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ftp: mkdir %s: %w", dir, err)
	}

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("ftp: create %s: %w", localPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp); err != nil {
		os.Remove(localPath)
		return fmt.Errorf("ftp: download %s: %w", fullPath, err)
	}
	return nil
}

func (b *ftpBackend) List(_ context.Context, prefix string) ([]RemoteSegment, error) {
	conn, err := b.connect()
	if err != nil {
		return nil, err
	}

	fullPath := path.Join(b.cfg.Path, prefix)
	return b.listRecursive(conn, fullPath, b.cfg.Path)
}

func (b *ftpBackend) listRecursive(conn *ftp.ServerConn, dir, basePath string) ([]RemoteSegment, error) {
	entries, err := conn.List(dir)
	if err != nil {
		return nil, fmt.Errorf("ftp: list %s: %w", dir, err)
	}

	var segments []RemoteSegment
	for _, entry := range entries {
		fullPath := dir + "/" + entry.Name
		if entry.Type == ftp.EntryTypeFolder {
			sub, err := b.listRecursive(conn, fullPath, basePath)
			if err != nil {
				continue
			}
			segments = append(segments, sub...)
		} else {
			relPath := strings.TrimPrefix(fullPath, basePath)
			relPath = strings.TrimPrefix(relPath, "/")
			segments = append(segments, RemoteSegment{
				Path:    relPath,
				Size:    int64(entry.Size),
				ModTime: entry.Time,
			})
		}
	}
	return segments, nil
}

func (b *ftpBackend) Delete(_ context.Context, remotePath string) error {
	conn, err := b.connect()
	if err != nil {
		return err
	}
	fullPath := path.Join(b.cfg.Path, remotePath)
	if err := conn.Delete(fullPath); err != nil {
		return fmt.Errorf("ftp: delete %s: %w", fullPath, err)
	}
	return nil
}

func (b *ftpBackend) Upload(_ context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("ftp: open %s: %w", localPath, err)
	}
	defer f.Close()

	conn, err := b.connect()
	if err != nil {
		return err
	}

	fullPath := path.Join(b.cfg.Path, remotePath)

	// Create parent directories
	dir := path.Dir(fullPath)
	if err := mkdirAll(conn, dir); err != nil {
		return fmt.Errorf("ftp: mkdir %s: %w", dir, err)
	}

	if err := conn.Stor(fullPath, f); err != nil {
		// Connection may have gone stale — drop it so next attempt reconnects
		b.mu.Lock()
		b.conn = nil
		b.mu.Unlock()
		return fmt.Errorf("ftp: stor %s: %w", fullPath, err)
	}
	return nil
}

// mkdirAll creates a directory path on the FTP server, ignoring "already exists" errors.
func mkdirAll(conn *ftp.ServerConn, dir string) error {
	if dir == "/" || dir == "." {
		return nil
	}

	parts := strings.Split(strings.Trim(dir, "/"), "/")
	current := ""
	for _, part := range parts {
		current += "/" + part
		err := conn.MakeDir(current)
		if err != nil {
			// Ignore "already exists" — FTP doesn't have a standard error code for this
			// so we just try and continue
			_ = err
		}
	}
	return nil
}

func (b *ftpBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		return b.conn.Quit()
	}
	return nil
}

func (b *ftpBackend) Name() string { return "ftp" }
