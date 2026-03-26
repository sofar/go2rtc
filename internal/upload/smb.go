package upload

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/hirochachacha/go-smb2"
)

// SMBConfig for SMB/CIFS backend.
type SMBConfig struct {
	Host         string `yaml:"host"`          // server address (host or host:port)
	Share        string `yaml:"share"`         // share name
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"` // path to file containing password
	Path         string `yaml:"path"`          // base path within the share
}

type smbBackend struct {
	cfg  *SMBConfig
	mu   sync.Mutex
	conn net.Conn
	sess *smb2.Session
	sh   *smb2.Share
}

func NewSMBBackend(cfg *SMBConfig) Backend {
	return &smbBackend{cfg: cfg}
}

func (b *smbBackend) connect() (*smb2.Share, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sh != nil {
		return b.sh, nil
	}

	host := b.cfg.Host
	if !strings.Contains(host, ":") {
		host += ":445"
	}

	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("smb: dial %s: %w", host, err)
	}

	password := resolvePassword(b.cfg.Password, b.cfg.PasswordFile)
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     b.cfg.Username,
			Password: password,
		},
	}

	sess, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smb: session: %w", err)
	}

	sh, err := sess.Mount(b.cfg.Share)
	if err != nil {
		sess.Logoff()
		conn.Close()
		return nil, fmt.Errorf("smb: mount %s: %w", b.cfg.Share, err)
	}

	b.conn = conn
	b.sess = sess
	b.sh = sh
	return sh, nil
}

func (b *smbBackend) disconnect() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sh != nil {
		b.sh.Umount()
		b.sh = nil
	}
	if b.sess != nil {
		b.sess.Logoff()
		b.sess = nil
	}
	if b.conn != nil {
		b.conn.Close()
		b.conn = nil
	}
}

func (b *smbBackend) Download(_ context.Context, remotePath, localPath string) error {
	sh, err := b.connect()
	if err != nil {
		return err
	}

	fullPath := path.Join(b.cfg.Path, remotePath)
	src, err := sh.Open(fullPath)
	if err != nil {
		return fmt.Errorf("smb: open remote %s: %w", fullPath, err)
	}
	defer src.Close()

	dir := path.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("smb: mkdir %s: %w", dir, err)
	}

	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("smb: create %s: %w", localPath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(localPath)
		return fmt.Errorf("smb: download %s: %w", fullPath, err)
	}
	return nil
}

func (b *smbBackend) List(_ context.Context, prefix string) ([]RemoteSegment, error) {
	sh, err := b.connect()
	if err != nil {
		return nil, err
	}

	fullPath := path.Join(b.cfg.Path, prefix)
	return b.listRecursive(sh, fullPath, b.cfg.Path)
}

func (b *smbBackend) listRecursive(sh *smb2.Share, dir, basePath string) ([]RemoteSegment, error) {
	entries, err := sh.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // directory doesn't exist yet
		}
		return nil, fmt.Errorf("smb: readdir %s: %w", dir, err)
	}

	var segments []RemoteSegment
	for _, entry := range entries {
		fullPath := dir + "/" + entry.Name()
		if entry.IsDir() {
			sub, err := b.listRecursive(sh, fullPath, basePath)
			if err != nil {
				continue
			}
			segments = append(segments, sub...)
		} else {
			relPath := strings.TrimPrefix(fullPath, basePath)
			relPath = strings.TrimPrefix(relPath, "/")
			segments = append(segments, RemoteSegment{
				Path:    relPath,
				Size:    entry.Size(),
				ModTime: entry.ModTime(),
			})
		}
	}
	return segments, nil
}

func (b *smbBackend) Delete(_ context.Context, remotePath string) error {
	sh, err := b.connect()
	if err != nil {
		return err
	}
	fullPath := path.Join(b.cfg.Path, remotePath)
	if err := sh.Remove(fullPath); err != nil {
		return fmt.Errorf("smb: delete %s: %w", fullPath, err)
	}
	return nil
}

func (b *smbBackend) Upload(_ context.Context, localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("smb: open %s: %w", localPath, err)
	}
	defer src.Close()

	sh, err := b.connect()
	if err != nil {
		return err
	}

	fullPath := path.Join(b.cfg.Path, remotePath)

	// Create parent directories
	dir := path.Dir(fullPath)
	if err := sh.MkdirAll(dir, 0o755); err != nil {
		// Reconnect on failure and retry once
		b.disconnect()
		sh, err = b.connect()
		if err != nil {
			return err
		}
		if err := sh.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("smb: mkdir %s: %w", dir, err)
		}
	}

	dst, err := sh.Create(fullPath)
	if err != nil {
		return fmt.Errorf("smb: create %s: %w", fullPath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		b.disconnect()
		return fmt.Errorf("smb: copy %s: %w", fullPath, err)
	}
	return nil
}

func (b *smbBackend) Close() error {
	b.disconnect()
	return nil
}

func (b *smbBackend) Name() string { return "smb" }
