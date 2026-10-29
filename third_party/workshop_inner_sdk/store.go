package clt01

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 把配对凭据持久化到本地 JSON 文件，按设备地址（host:port）索引。
// 文件含敏感密钥，权限为 0600。
type Store struct {
	Path string
	mu   sync.Mutex
}

// DefaultStorePath 返回默认凭据文件路径：$HOME/.clt01sdk/credentials.json。
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clt01sdk", "credentials.json"), nil
}

// NewStore 使用默认路径创建 Store。
func NewStore() (*Store, error) {
	p, err := DefaultStorePath()
	if err != nil {
		return nil, err
	}
	return &Store{Path: p}, nil
}

func (s *Store) load() (map[string]*Credentials, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*Credentials{}, nil
		}
		return nil, err
	}
	out := map[string]*Credentials{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("clt01: 凭据文件损坏 %s: %w", s.Path, err)
	}
	return out, nil
}

func (s *Store) save(m map[string]*Credentials) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, raw, 0600)
}

// Save 保存（覆盖）一台设备的凭据。
func (s *Store) Save(creds *Credentials) error {
	if creds == nil || creds.DeviceAddr == "" {
		return fmt.Errorf("clt01: 凭据缺少 DeviceAddr")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	m[creds.DeviceAddr] = creds
	return s.save(m)
}

// Load 读取一台设备的凭据；不存在返回 nil, nil。
func (s *Store) Load(deviceAddr string) (*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	return m[deviceAddr], nil
}

// Delete 删除一台设备的凭据。
func (s *Store) Delete(deviceAddr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, deviceAddr)
	return s.save(m)
}

// ClientFromStore 从本地凭据构建已授权客户端；未配对返回错误。
func ClientFromStore(deviceAddr string) (*Client, error) {
	c, err := New(deviceAddr)
	if err != nil {
		return nil, err
	}
	s, err := NewStore()
	if err != nil {
		return nil, err
	}
	creds, err := s.Load(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("clt01: %s 尚未配对（请先调用 Pair）", deviceAddr)
	}
	if creds.SessionExpired() {
		return nil, fmt.Errorf("clt01: %s 的 session 凭据已过期，请重新配对", deviceAddr)
	}
	c.Apply(creds)
	return c, nil
}
