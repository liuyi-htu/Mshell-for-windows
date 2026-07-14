//go:build windows
// +build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	gwlStyle      = ^uintptr(15)
	swMinimize    = 6
	swPNoMove     = 0x0002
	swPNoSize     = 0x0001
	swPNoZOrder   = 0x0004
	swPFrameChged = 0x0020
	swPShowWindow = 0x0040

	monitorDefaultToNearest = 2
	wmNCLButtonDown         = 0x00A1
	htCaption               = 2
	wsCaption               = 0x00C00000
	wsSysMenu               = 0x00080000
	wsMinimizeBox           = 0x00020000
	wsMaximizeBox           = 0x00010000
	wsThickFrame            = 0x00040000
)

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetFileAttributes = kernel32.NewProc("GetFileAttributesW")
	procGetWindowLongPtr  = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr  = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procShowWindow        = user32.NewProc("ShowWindow")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo    = user32.NewProc("GetMonitorInfoW")
	procReleaseCapture    = user32.NewProc("ReleaseCapture")
	procSendMessage       = user32.NewProc("SendMessageW")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
)

//go:embed static/index.html
var indexHTML []byte

//go:embed static
var staticFiles embed.FS

type config struct {
	addr        string
	token       string
	autoConnect string
}

type appState struct {
	mu        sync.Mutex
	sessions  map[string]*remoteSession
	transfers map[string]*transferTask
}

type remoteSession struct {
	id       string
	label    string
	username string
	home     string
	ssh      *ssh.Client
	sftp     *sftp.Client
	lastUsed time.Time
}

type loginRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type savedAccount struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type listResponse struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []fileEntry `json:"entries"`
}

type fileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
	Modified string `json:"modified"`
	Mode     string `json:"mode"`
	IsDir    bool   `json:"isDir"`
	Hidden   bool   `json:"hidden"`
}

type transferRequest struct {
	SessionID  string `json:"sessionId"`
	LocalPath  string `json:"localPath"`
	RemotePath string `json:"remotePath"`
}

type transferStartRequest struct {
	SourceKind      string `json:"sourceKind"`
	DestKind        string `json:"destKind"`
	SourceSessionID string `json:"sourceSessionId"`
	DestSessionID   string `json:"destSessionId"`
	SourcePath      string `json:"sourcePath"`
	DestPath        string `json:"destPath"`
	Name            string `json:"name"`
}

type transferIDRequest struct {
	ID string `json:"id"`
}

type transferTask struct {
	mu       sync.Mutex
	id       string
	name     string
	status   string
	done     int64
	total    int64
	err      string
	started  time.Time
	finished time.Time
	cancel   context.CancelFunc
}

type transferStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Error    string `json:"error"`
	Elapsed  int64  `json:"elapsed"`
	Finished bool   `json:"finished"`
}

type pathRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

type renameRequest struct {
	SessionID string `json:"sessionId"`
	OldPath   string `json:"oldPath"`
	NewPath   string `json:"newPath"`
}

type copyRequest struct {
	SessionID  string `json:"sessionId"`
	SourcePath string `json:"sourcePath"`
	DestPath   string `json:"destPath"`
}

type chmodRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
}

type windowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type windowPoint struct {
	X int32
	Y int32
}

type monitorInfo struct {
	Size    uint32
	Monitor windowRect
	Work    windowRect
	Flags   uint32
}

var (
	windowMaximized bool
	windowRestore   windowRect
)

func main() {
	cfg := config{}
	openOnStart := false
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:3161", "HTTP listen address")
	flag.StringVar(&cfg.token, "token", "", "optional access token")
	flag.StringVar(&cfg.autoConnect, "autoconnect", "", "base64url encoded account JSON to connect on startup")
	flag.BoolVar(&openOnStart, "open", false, "open the app in a desktop window")
	flag.Parse()

	state := &appState{sessions: map[string]*remoteSession{}, transfers: map[string]*transferTask{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex(cfg))
	mux.HandleFunc("/api/connect", state.serveConnect(cfg))
	mux.HandleFunc("/api/disconnect", state.serveDisconnect(cfg))
	mux.HandleFunc("/api/local", serveLocal(cfg))
	mux.HandleFunc("/api/local-special", serveLocalSpecial(cfg))
	mux.HandleFunc("/api/remote", state.serveRemote(cfg))
	mux.HandleFunc("/api/upload", state.serveUpload(cfg))
	mux.HandleFunc("/api/download", state.serveDownload(cfg))
	mux.HandleFunc("/api/transfer/start", state.serveTransferStart(cfg))
	mux.HandleFunc("/api/transfer/status", state.serveTransferStatus(cfg))
	mux.HandleFunc("/api/transfer/cancel", state.serveTransferCancel(cfg))
	mux.HandleFunc("/api/remote-mkdir", state.serveRemoteMkdir(cfg))
	mux.HandleFunc("/api/remote-delete", state.serveRemoteDelete(cfg))
	mux.HandleFunc("/api/remote-rename", state.serveRemoteRename(cfg))
	mux.HandleFunc("/api/remote-copy", state.serveRemoteCopy(cfg))
	mux.HandleFunc("/api/remote-chmod", state.serveRemoteChmod(cfg))
	mux.HandleFunc("/api/remote-open", state.serveRemoteOpen(cfg))
	mux.HandleFunc("/api/remote-edit", state.serveRemoteEdit(cfg))
	mux.HandleFunc("/api/local-delete", serveLocalDelete(cfg))
	mux.HandleFunc("/api/local-rename", serveLocalRename(cfg))
	mux.HandleFunc("/api/local-copy", serveLocalCopy(cfg))
	mux.HandleFunc("/api/local-chmod", serveLocalChmod(cfg))
	mux.HandleFunc("/api/local-open", serveLocalOpen(cfg))
	mux.HandleFunc("/api/local-edit", serveLocalEdit(cfg))
	mux.HandleFunc("/accounts", serveAccounts(cfg))
	if sub, err := fs.Sub(staticFiles, "static"); err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	server := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	appURL := appURL(cfg)
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		if openOnStart {
			runAppWindow(appURL)
			return
		}
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("msftp listening on %s", appURL)
	if openOnStart {
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				log.Printf("server stopped: %v", err)
			}
		}()
		runAppWindow(appURL)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		return
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func appURL(cfg config) string {
	values := url.Values{}
	if cfg.token != "" {
		values.Set("token", cfg.token)
	}
	if strings.TrimSpace(cfg.autoConnect) != "" {
		values.Set("autoconnect", strings.TrimSpace(cfg.autoConnect))
	}
	u := "http://" + cfg.addr + "/"
	if encoded := values.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return u
}

func serveIndex(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !authorized(r, cfg.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		_, _ = w.Write(indexHTML)
	}
}

func serveAccounts(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, cfg.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			accounts, err := loadSavedAccounts()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(accounts)
		case http.MethodPost:
			defer r.Body.Close()
			accounts := []savedAccount{}
			if err := json.NewDecoder(r.Body).Decode(&accounts); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := saveSavedAccounts(accounts); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func loadSavedAccounts() ([]savedAccount, error) {
	data, err := os.ReadFile(accountsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []savedAccount{}, nil
		}
		return nil, err
	}
	accounts := []savedAccount{}
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, err
	}
	migrationNeeded, err := decryptSavedAccountPasswords(accounts)
	if err != nil {
		return nil, err
	}
	if migrationNeeded {
		if err := saveSavedAccounts(accounts); err != nil {
			return nil, fmt.Errorf("migrate saved passwords to Windows DPAPI: %w", err)
		}
	}
	sortSavedAccounts(accounts)
	return accounts, nil
}

func saveSavedAccounts(accounts []savedAccount) error {
	path := accountsPath()
	if err := ensureWritableDir(filepath.Dir(path)); err != nil {
		return err
	}
	sortSavedAccounts(accounts)
	encryptedAccounts, err := encryptSavedAccountPasswords(accounts)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(encryptedAccounts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func sortSavedAccounts(accounts []savedAccount) {
	sort.SliceStable(accounts, func(i, j int) bool {
		left := strings.ToLower(accountSortName(accounts[i]))
		right := strings.ToLower(accountSortName(accounts[j]))
		if left == right {
			return accounts[i].Host < accounts[j].Host
		}
		return left < right
	})
}

func accountSortName(account savedAccount) string {
	name := strings.TrimSpace(account.Name)
	if name != "" {
		return name
	}
	port := strings.TrimSpace(account.Port)
	if port == "" {
		port = "22"
	}
	return strings.TrimSpace(account.Username) + "@" + strings.TrimSpace(account.Host) + ":" + port
}

func accountsPath() string {
	return filepath.Join(sharedDataDir(), "accounts.json")
}

func (s *appState) serveConnect(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req loginRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		host := strings.TrimSpace(req.Host)
		port := strings.TrimSpace(req.Port)
		username := strings.TrimSpace(req.Username)
		if port == "" {
			port = "22"
		}
		if host == "" || username == "" {
			http.Error(w, "host and username are required", http.StatusBadRequest)
			return
		}
		if _, err := strconv.Atoi(port); err != nil {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		hostKeyCallback, err := verifiedHostKeyCallback()
		if err != nil {
			http.Error(w, "failed to initialize SSH host key verification: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sshClient, err := ssh.Dial("tcp", net.JoinHostPort(host, port), &ssh.ClientConfig{
			User:            username,
			Auth:            []ssh.AuthMethod{ssh.Password(req.Password)},
			HostKeyCallback: hostKeyCallback,
			Timeout:         10 * time.Second,
		})
		if err != nil {
			http.Error(w, "ssh connect failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		sftpClient, err := newSFTPClientWithTimeout(sshClient, 10*time.Second)
		if err != nil {
			_ = sshClient.Close()
			http.Error(w, "sftp start failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		id := randomID()
		label := strings.TrimSpace(req.Name)
		if label == "" {
			label = username + "@" + host
		}
		home := remoteHomePath(sftpClient, username)
		session := &remoteSession{id: id, label: label, username: username, home: home, ssh: sshClient, sftp: sftpClient, lastUsed: time.Now()}
		s.mu.Lock()
		s.sessions[id] = session
		s.mu.Unlock()
		writeJSON(w, map[string]string{"sessionId": id, "label": label, "home": home})
	}
}

func remoteHomePath(client *sftp.Client, _ string) string {
	// Do not assume that the SSH username is also the home directory name.
	// Let the SFTP server resolve the account's actual initial directory.
	home, err := remoteRealPathWithTimeout(client, ".", 3*time.Second)
	if err != nil {
		return "/"
	}

	home = path.Clean(strings.TrimSpace(home))
	if home == "" || home == "." {
		return "/"
	}
	return home
}

func resolveRemotePath(session *remoteSession, requested string) string {
	value := strings.TrimSpace(requested)
	home := strings.TrimSpace(session.home)
	if home == "" {
		home = "."
	}
	if value == "" || value == "." || value == "~" {
		return path.Clean(home)
	}
	if strings.HasPrefix(value, "~/") {
		return path.Clean(path.Join(home, strings.TrimPrefix(value, "~/")))
	}
	if session.username == "root" && strings.EqualFold(strings.Trim(value, "/"), "root") {
		return "/root"
	}
	return cleanRemotePath(value)
}

func newSFTPClientWithTimeout(sshClient *ssh.Client, timeout time.Duration) (*sftp.Client, error) {
	type result struct {
		client *sftp.Client
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client, err := sftp.NewClient(sshClient)
		done <- result{client: client, err: err}
	}()
	select {
	case result := <-done:
		return result.client, result.err
	case <-time.After(timeout):
		return nil, errors.New("sftp start timed out")
	}
}

func remoteRealPathWithTimeout(client *sftp.Client, target string, timeout time.Duration) (string, error) {
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		value, err := client.RealPath(target)
		done <- result{path: value, err: err}
	}()
	select {
	case result := <-done:
		return result.path, result.err
	case <-time.After(timeout):
		return "", errors.New("remote path query timed out")
	}
}

func remoteReadDirWithTimeout(client *sftp.Client, target string, timeout time.Duration) ([]os.FileInfo, error) {
	type result struct {
		infos []os.FileInfo
		err   error
	}
	done := make(chan result, 1)
	go func() {
		infos, err := client.ReadDir(target)
		done <- result{infos: infos, err: err}
	}()
	select {
	case result := <-done:
		return result.infos, result.err
	case <-time.After(timeout):
		return nil, errors.New("remote directory read timed out")
	}
}

func (s *appState) serveDisconnect(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session := s.remove(req.SessionID)
		if session != nil {
			_ = session.sftp.Close()
			_ = session.ssh.Close()
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func serveLocalSpecial(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodGet) {
			return
		}
		home, _ := os.UserHomeDir()
		paths := map[string]string{
			"computer":  "",
			"desktop":   filepath.Join(home, "Desktop"),
			"downloads": filepath.Join(home, "Downloads"),
			"documents": filepath.Join(home, "Documents"),
		}
		writeJSON(w, paths)
	}
}

func serveLocal(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodGet) {
			return
		}
		requested := strings.TrimSpace(r.URL.Query().Get("path"))
		if requested == "" {
			requested = defaultLocalPath()
		}
		cleaned, err := filepath.Abs(requested)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		entries, err := os.ReadDir(cleaned)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		files := make([]fileEntry, 0, len(entries))
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			entryPath := filepath.Join(cleaned, entry.Name())
			files = append(files, fileEntry{
				Name:     entry.Name(),
				Path:     entryPath,
				Size:     info.Size(),
				Type:     localType(info),
				Modified: formatTime(info.ModTime()),
				Mode:     info.Mode().String(),
				IsDir:    info.IsDir(),
				Hidden:   isHiddenLocal(entryPath, entry.Name()),
			})
		}
		sortEntries(files)
		parent := filepath.Dir(cleaned)
		if parent == cleaned {
			parent = ""
		}
		writeJSON(w, listResponse{Path: cleaned, Parent: parent, Entries: files})
	}
}

func serveLocalDelete(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := os.RemoveAll(req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func serveLocalRename(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req renameRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.OldPath) == "" || strings.TrimSpace(req.NewPath) == "" {
			http.Error(w, "oldPath and newPath are required", http.StatusBadRequest)
			return
		}
		if err := os.Rename(req.OldPath, req.NewPath); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func serveLocalCopy(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req copyRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.DestPath) == "" {
			http.Error(w, "sourcePath and destPath are required", http.StatusBadRequest)
			return
		}
		destPath, err := uniqueLocalPath(req.DestPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bytes, err := copyLocal(req.SourcePath, destPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "bytes": bytes, "path": destPath})
	}
}

func serveLocalChmod(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req chmodRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		mode, err := parseOctalMode(req.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := os.Chmod(req.Path, mode); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func serveLocalOpen(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := openWithDefaultApp(req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func serveLocalEdit(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		info, err := os.Stat(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if info.IsDir() {
			http.Error(w, "cannot edit a folder with notepad", http.StatusBadRequest)
			return
		}
		if err := openWithNotepad(req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemote(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodGet) {
			return
		}
		session, ok := s.get(r.URL.Query().Get("sessionId"))
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		requested := strings.TrimSpace(r.URL.Query().Get("path"))
		cleaned := resolveRemotePath(session, requested)
		infos, err := remoteReadDirWithTimeout(session.sftp, cleaned, 12*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		files := make([]fileEntry, 0, len(infos)+1)
		for _, info := range infos {
			files = append(files, fileEntry{
				Name:     info.Name(),
				Path:     path.Join(cleaned, info.Name()),
				Size:     info.Size(),
				Type:     remoteType(info),
				Modified: formatTime(info.ModTime()),
				Mode:     info.Mode().String(),
				IsDir:    info.IsDir(),
				Hidden:   isHiddenRemote(info.Name()),
			})
		}
		sortEntries(files)
		parent := path.Dir(cleaned)
		if parent == cleaned || cleaned == "/" {
			parent = ""
		}
		writeJSON(w, listResponse{Path: cleaned, Parent: parent, Entries: files})
	}
}

func (s *appState) serveUpload(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req transferRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		localInfo, err := os.Stat(req.LocalPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		remoteTarget := req.RemotePath
		if remoteTarget == "" || strings.HasSuffix(remoteTarget, "/") {
			remoteTarget = path.Join(remoteTarget, filepath.Base(req.LocalPath))
		}
		if localInfo.IsDir() {
			err = uploadDir(session.sftp, req.LocalPath, remoteTarget)
		} else {
			err = uploadFile(session.sftp, req.LocalPath, remoteTarget)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "bytes": localInfo.Size()})
	}
}

func (s *appState) serveDownload(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req transferRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		info, err := session.sftp.Stat(req.RemotePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		localTarget := req.LocalPath
		if localTarget == "" || strings.HasSuffix(localTarget, string(filepath.Separator)) {
			localTarget = filepath.Join(localTarget, path.Base(req.RemotePath))
		}
		if info.IsDir() {
			err = downloadDir(session.sftp, req.RemotePath, localTarget)
		} else {
			err = downloadFile(session.sftp, req.RemotePath, localTarget)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "bytes": info.Size()})
	}
}

func (s *appState) serveRemoteMkdir(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := session.sftp.MkdirAll(req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemoteDelete(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" || req.Path == "/" {
			http.Error(w, "refusing to delete this path", http.StatusBadRequest)
			return
		}
		if err := removeRemote(session.sftp, req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemoteRename(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req renameRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.OldPath) == "" || strings.TrimSpace(req.NewPath) == "" {
			http.Error(w, "oldPath and newPath are required", http.StatusBadRequest)
			return
		}
		if err := session.sftp.Rename(req.OldPath, req.NewPath); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemoteCopy(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req copyRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.DestPath) == "" {
			http.Error(w, "sourcePath and destPath are required", http.StatusBadRequest)
			return
		}
		destPath, err := uniqueRemotePath(session.sftp, req.DestPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bytes, err := copyRemote(session.sftp, req.SourcePath, destPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "bytes": bytes, "path": destPath})
	}
}

func (s *appState) serveRemoteChmod(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req chmodRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		mode, err := parseOctalMode(req.Mode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if err := session.sftp.Chmod(req.Path, mode); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemoteOpen(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		info, err := session.sftp.Stat(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if info.IsDir() {
			http.Error(w, "folder should be opened in the file list", http.StatusBadRequest)
			return
		}
		localPath, err := downloadRemoteToTemp(session.sftp, req.Path, "msftp-open-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := openWithDefaultApp(localPath); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveRemoteEdit(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req pathRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		session, ok := s.get(req.SessionID)
		if !ok {
			http.Error(w, "not connected", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		info, err := session.sftp.Stat(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if info.IsDir() {
			http.Error(w, "cannot edit a folder with notepad", http.StatusBadRequest)
			return
		}
		localPath, err := downloadRemoteToTemp(session.sftp, req.Path, "msftp-edit-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("notepad.exe", localPath)
		if err := cmd.Start(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		go func(remotePath, tempPath string) {
			if err := cmd.Wait(); err == nil {
				if _, statErr := os.Stat(tempPath); statErr == nil {
					_ = uploadFile(session.sftp, tempPath, remotePath)
				}
			}
			_ = os.RemoveAll(filepath.Dir(tempPath))
		}(req.Path, localPath)
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *appState) serveTransferStart(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req transferStartRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		req.SourceKind = strings.ToLower(strings.TrimSpace(req.SourceKind))
		req.DestKind = strings.ToLower(strings.TrimSpace(req.DestKind))
		if req.SourceKind != "local" && req.SourceKind != "remote" {
			http.Error(w, "invalid source kind", http.StatusBadRequest)
			return
		}
		if req.DestKind != "local" && req.DestKind != "remote" {
			http.Error(w, "invalid destination kind", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.DestPath) == "" {
			http.Error(w, "sourcePath and destPath are required", http.StatusBadRequest)
			return
		}

		var srcSession *remoteSession
		var dstSession *remoteSession
		var ok bool
		if req.SourceKind == "remote" {
			srcSession, ok = s.get(req.SourceSessionID)
			if !ok {
				http.Error(w, "source server is not connected", http.StatusBadRequest)
				return
			}
		}
		if req.DestKind == "remote" {
			dstSession, ok = s.get(req.DestSessionID)
			if !ok {
				http.Error(w, "destination server is not connected", http.StatusBadRequest)
				return
			}
		}

		name := strings.TrimSpace(req.Name)
		if name == "" {
			if req.SourceKind == "local" {
				name = filepath.Base(req.SourcePath)
			} else {
				name = path.Base(req.SourcePath)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		task := &transferTask{id: randomID(), name: name, status: "running", started: time.Now(), cancel: cancel}
		s.addTransfer(task)
		go func() {
			var total int64
			var err error
			switch req.SourceKind {
			case "local":
				total, err = localPathSize(ctx, req.SourcePath)
			case "remote":
				total, err = remotePathSize(ctx, srcSession.sftp, req.SourcePath)
			}
			if err == nil {
				task.setTotal(total)
				err = performTransfer(ctx, req, srcSession, dstSession, task.addDone)
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					task.finish("canceled", "canceled")
				} else {
					task.finish("failed", err.Error())
				}
				return
			}
			task.finish("done", "")
		}()
		writeJSON(w, transferStatus{ID: task.id, Name: task.name, Status: task.status})
	}
}

func (s *appState) serveTransferStatus(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodGet) {
			return
		}
		task := s.getTransfer(r.URL.Query().Get("id"))
		if task == nil {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		writeJSON(w, task.snapshot())
	}
}

func (s *appState) serveTransferCancel(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiAllowed(w, r, cfg, http.MethodPost) {
			return
		}
		var req transferIDRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		task := s.getTransfer(req.ID)
		if task == nil {
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		task.mu.Lock()
		cancel := task.cancel
		if task.status == "running" {
			task.status = "canceling"
		}
		task.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		writeJSON(w, task.snapshot())
	}
}

func (s *appState) addTransfer(task *transferTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transfers == nil {
		s.transfers = map[string]*transferTask{}
	}
	s.transfers[task.id] = task
	if len(s.transfers) > 200 {
		cutoff := time.Now().Add(-2 * time.Hour)
		for id, item := range s.transfers {
			item.mu.Lock()
			finished := !item.finished.IsZero() && item.finished.Before(cutoff)
			item.mu.Unlock()
			if finished {
				delete(s.transfers, id)
			}
		}
	}
}

func (s *appState) getTransfer(id string) *transferTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfers[id]
}

func (t *transferTask) setTotal(total int64) {
	t.mu.Lock()
	t.total = total
	t.mu.Unlock()
}

func (t *transferTask) addDone(n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.done += n
	t.mu.Unlock()
}

func (t *transferTask) finish(status, message string) {
	t.mu.Lock()
	t.status = status
	t.err = message
	t.finished = time.Now()
	t.mu.Unlock()
}

func (t *transferTask) snapshot() transferStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := int64(time.Since(t.started).Seconds())
	if !t.finished.IsZero() {
		elapsed = int64(t.finished.Sub(t.started).Seconds())
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return transferStatus{
		ID:       t.id,
		Name:     t.name,
		Status:   t.status,
		Done:     t.done,
		Total:    t.total,
		Error:    t.err,
		Elapsed:  elapsed,
		Finished: !t.finished.IsZero(),
	}
}

func performTransfer(ctx context.Context, req transferStartRequest, srcSession, dstSession *remoteSession, progress func(int64)) error {
	switch req.SourceKind + "->" + req.DestKind {
	case "local->remote":
		return copyLocalToRemote(ctx, req.SourcePath, req.DestPath, dstSession.sftp, progress)
	case "remote->local":
		return copyRemoteToLocal(ctx, srcSession.sftp, req.SourcePath, req.DestPath, progress)
	case "remote->remote":
		return copyRemoteToRemote(ctx, srcSession.sftp, req.SourcePath, dstSession.sftp, req.DestPath, progress)
	case "local->local":
		return copyLocalToLocal(ctx, req.SourcePath, req.DestPath, progress)
	default:
		return errors.New("unsupported transfer direction")
	}
}

func localPathSize(ctx context.Context, target string) (int64, error) {
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(target, func(current string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func remotePathSize(ctx context.Context, client *sftp.Client, target string) (int64, error) {
	info, err := client.Stat(target)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	walker := client.Walk(target)
	for walker.Step() {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if err := walker.Err(); err != nil {
			return total, err
		}
		if !walker.Stat().IsDir() {
			total += walker.Stat().Size()
		}
	}
	return total, nil
}

func copyLocalToRemote(ctx context.Context, localPath, remotePath string, client *sftp.Client, progress func(int64)) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyLocalFileToRemote(ctx, localPath, remotePath, client, progress)
	}
	return filepath.WalkDir(localPath, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(localPath, current)
		if err != nil {
			return err
		}
		target := remotePath
		if rel != "." {
			target = path.Join(remotePath, filepath.ToSlash(rel))
		}
		if d.IsDir() {
			return client.MkdirAll(target)
		}
		return copyLocalFileToRemote(ctx, current, target, client, progress)
	})
}

func copyLocalFileToRemote(ctx context.Context, localPath, remotePath string, client *sftp.Client, progress func(int64)) error {
	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, _ := in.Stat()
	if err := client.MkdirAll(path.Dir(remotePath)); err != nil {
		return err
	}
	out, err := client.Create(remotePath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = copyStream(ctx, in, out, progress)
	if info != nil && err == nil {
		if chmodErr := client.Chmod(remotePath, info.Mode()); chmodErr != nil {
			err = chmodErr
		}
	}
	if err != nil && errors.Is(err, context.Canceled) {
		_ = client.Remove(remotePath)
	}
	return err
}

func copyRemoteToLocal(ctx context.Context, client *sftp.Client, remotePath, localPath string, progress func(int64)) error {
	info, err := client.Stat(remotePath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyRemoteFileToLocal(ctx, client, remotePath, localPath, progress)
	}
	walker := client.Walk(remotePath)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), remotePath), "/")
		target := localPath
		if rel != "" {
			target = filepath.Join(localPath, filepath.FromSlash(rel))
		}
		if walker.Stat().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := copyRemoteFileToLocal(ctx, client, walker.Path(), target, progress); err != nil {
			return err
		}
	}
	return nil
}

func copyRemoteFileToLocal(ctx context.Context, client *sftp.Client, remotePath, localPath string, progress func(int64)) error {
	in, err := client.Open(remotePath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = copyStream(ctx, in, out, progress)
	if err != nil && errors.Is(err, context.Canceled) {
		_ = os.Remove(localPath)
	}
	return err
}

func copyRemoteToRemote(ctx context.Context, srcClient *sftp.Client, srcPath string, dstClient *sftp.Client, dstPath string, progress func(int64)) error {
	info, err := srcClient.Stat(srcPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyRemoteFileToRemote(ctx, srcClient, srcPath, dstClient, dstPath, progress)
	}
	walker := srcClient.Walk(srcPath)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), srcPath), "/")
		target := dstPath
		if rel != "" {
			target = path.Join(dstPath, rel)
		}
		if walker.Stat().IsDir() {
			if err := dstClient.MkdirAll(target); err != nil {
				return err
			}
			continue
		}
		if err := copyRemoteFileToRemote(ctx, srcClient, walker.Path(), dstClient, target, progress); err != nil {
			return err
		}
	}
	return nil
}

func copyRemoteFileToRemote(ctx context.Context, srcClient *sftp.Client, srcPath string, dstClient *sftp.Client, dstPath string, progress func(int64)) error {
	in, err := srcClient.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, _ := srcClient.Stat(srcPath)
	if err := dstClient.MkdirAll(path.Dir(dstPath)); err != nil {
		return err
	}
	out, err := dstClient.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = copyStream(ctx, in, out, progress)
	if info != nil && err == nil {
		if chmodErr := dstClient.Chmod(dstPath, info.Mode()); chmodErr != nil {
			err = chmodErr
		}
	}
	if err != nil && errors.Is(err, context.Canceled) {
		_ = dstClient.Remove(dstPath)
	}
	return err
}

func copyLocalToLocal(ctx context.Context, src, dst string, progress func(int64)) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyLocalFileToLocal(ctx, src, dst, info.Mode(), progress)
	}
	return filepath.WalkDir(src, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, current)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		entryInfo, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, entryInfo.Mode())
		}
		return copyLocalFileToLocal(ctx, current, target, entryInfo.Mode(), progress)
	})
}

func copyLocalFileToLocal(ctx context.Context, src, dst string, mode os.FileMode, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = copyStream(ctx, in, out, progress)
	if err != nil && errors.Is(err, context.Canceled) {
		_ = os.Remove(dst)
	}
	return err
}

func copyStream(ctx context.Context, src io.Reader, dst io.Writer, progress func(int64)) (int64, error) {
	buf := make([]byte, 1024*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			if err := ctx.Err(); err != nil {
				return written, err
			}
			chunk := buf[:nr]
			for len(chunk) > 0 {
				nw, ew := dst.Write(chunk)
				if nw > 0 {
					written += int64(nw)
					progress(int64(nw))
					chunk = chunk[nw:]
				}
				if ew != nil {
					return written, ew
				}
				if nw == 0 {
					return written, io.ErrShortWrite
				}
				if err := ctx.Err(); err != nil {
					return written, err
				}
			}
		}
		if er != nil {
			if er == io.EOF {
				return written, nil
			}
			return written, er
		}
	}
}

func (s *appState) get(id string) (*remoteSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if ok {
		session.lastUsed = time.Now()
	}
	return session, ok
}

func (s *appState) remove(id string) *remoteSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	delete(s.sessions, id)
	return session
}

func uploadFile(client *sftp.Client, localPath, remotePath string) error {
	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := client.MkdirAll(path.Dir(remotePath)); err != nil {
		return err
	}
	out, err := client.Create(remotePath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func uploadDir(client *sftp.Client, localRoot, remoteRoot string) error {
	return filepath.WalkDir(localRoot, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(localRoot, current)
		if err != nil {
			return err
		}
		target := remoteRoot
		if rel != "." {
			target = path.Join(remoteRoot, filepath.ToSlash(rel))
		}
		if d.IsDir() {
			return client.MkdirAll(target)
		}
		return uploadFile(client, current, target)
	})
}

func downloadFile(client *sftp.Client, remotePath, localPath string) error {
	in, err := client.Open(remotePath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func downloadDir(client *sftp.Client, remoteRoot, localRoot string) error {
	walker := client.Walk(remoteRoot)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), remoteRoot), "/")
		target := localRoot
		if rel != "" {
			target = filepath.Join(localRoot, filepath.FromSlash(rel))
		}
		if walker.Stat().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := downloadFile(client, walker.Path(), target); err != nil {
			return err
		}
	}
	return nil
}

func removeRemote(client *sftp.Client, remotePath string) error {
	info, err := client.Stat(remotePath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return client.Remove(remotePath)
	}
	entries, err := client.ReadDir(remotePath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeRemote(client, path.Join(remotePath, entry.Name())); err != nil {
			return err
		}
	}
	return client.RemoveDirectory(remotePath)
}

func copyLocal(src, dst string) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		in, err := os.Open(src)
		if err != nil {
			return 0, err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return 0, err
		}
		defer out.Close()
		bytes, err := io.Copy(out, in)
		return bytes, err
	}
	var total int64
	err = filepath.WalkDir(src, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, current)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		entryInfo, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, entryInfo.Mode())
		}
		bytes, err := copyLocal(current, target)
		total += bytes
		return err
	})
	return total, err
}

func copyRemote(client *sftp.Client, src, dst string) (int64, error) {
	info, err := client.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if err := client.MkdirAll(path.Dir(dst)); err != nil {
			return 0, err
		}
		in, err := client.Open(src)
		if err != nil {
			return 0, err
		}
		defer in.Close()
		out, err := client.Create(dst)
		if err != nil {
			return 0, err
		}
		defer out.Close()
		bytes, err := io.Copy(out, in)
		if chmodErr := client.Chmod(dst, info.Mode()); err == nil && chmodErr != nil {
			err = chmodErr
		}
		return bytes, err
	}
	if err := client.MkdirAll(dst); err != nil {
		return 0, err
	}
	entries, err := client.ReadDir(src)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		bytes, err := copyRemote(client, path.Join(src, entry.Name()), path.Join(dst, entry.Name()))
		total += bytes
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func uniqueLocalPath(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("target path is required")
	}
	if _, err := os.Stat(target); isPathNotExist(err) {
		return target, nil
	} else if err != nil {
		return "", err
	}
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	for i := 1; i < 1000; i++ {
		suffix := " - 副本"
		if i > 1 {
			suffix = fmtCopySuffix(i)
		}
		candidate := filepath.Join(dir, name+suffix+ext)
		if _, err := os.Stat(candidate); isPathNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many duplicate file names")
}

func uniqueRemotePath(client *sftp.Client, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("target path is required")
	}
	if _, err := client.Stat(target); isPathNotExist(err) {
		return target, nil
	} else if err != nil {
		return "", err
	}
	dir := path.Dir(target)
	base := path.Base(target)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	for i := 1; i < 1000; i++ {
		suffix := " - 副本"
		if i > 1 {
			suffix = fmtCopySuffix(i)
		}
		candidate := path.Join(dir, name+suffix+ext)
		if _, err := client.Stat(candidate); isPathNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many duplicate file names")
}

func fmtCopySuffix(index int) string {
	return " - 副本 (" + strconv.Itoa(index) + ")"
}

func isPathNotExist(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file") || strings.Contains(message, "not exist")
}

func parseOctalMode(value string) (os.FileMode, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "0o")
	value = strings.TrimPrefix(value, "0O")
	value = strings.TrimPrefix(value, "0")
	if value == "" {
		return 0, errors.New("mode is required, for example 755 or 0644")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, errors.New("mode must be an octal number, for example 755 or 0644")
	}
	return os.FileMode(parsed), nil
}

func downloadRemoteToTemp(client *sftp.Client, remotePath, pattern string) (string, error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	localPath := filepath.Join(dir, path.Base(remotePath))
	if err := downloadFile(client, remotePath, localPath); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return localPath, nil
}

func openWithDefaultApp(target string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target).Start()
}

func openWithNotepad(target string) error {
	return exec.Command("notepad.exe", target).Start()
}

func cleanRemotePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "."
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return path.Clean("/" + value)
}

func isHiddenLocal(fullPath, name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	ptr, err := syscall.UTF16PtrFromString(fullPath)
	if err != nil {
		return false
	}
	attrs, _, _ := procGetFileAttributes.Call(uintptr(unsafe.Pointer(ptr)))
	if attrs == uintptr(^uint32(0)) {
		return false
	}
	const fileAttributeHidden = 0x2
	return attrs&fileAttributeHidden != 0
}

func isHiddenRemote(name string) bool {
	return strings.HasPrefix(name, ".")
}

func sortEntries(entries []fileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

func localType(info os.FileInfo) string {
	if info.IsDir() {
		return "文件夹"
	}
	return "文件"
}

func remoteType(info os.FileInfo) string {
	if info.IsDir() {
		return "文件夹"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "链接"
	}
	return "文件"
}

func formatTime(t time.Time) string {
	return t.Format("2006/1/2 15:04")
}

func defaultLocalPath() string {
	home, err := os.UserHomeDir()
	if err == nil {
		desktop := filepath.Join(home, "Desktop")
		if info, statErr := os.Stat(desktop); statErr == nil && info.IsDir() {
			return desktop
		}
		return home
	}
	wd, err := os.Getwd()
	if err == nil {
		return wd
	}
	return "."
}

func apiAllowed(w http.ResponseWriter, r *http.Request, cfg config, method string) bool {
	if !authorized(r, cfg.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if got == "" {
		if value := r.Header.Get("Authorization"); strings.HasPrefix(value, "Bearer ") {
			got = strings.TrimPrefix(value, "Bearer ")
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func runAppWindow(target string) {
	width, height := initialWindowSize()
	w := webview.NewWithOptions(webview.WebViewOptions{
		DataPath:  webviewDataDir(),
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  "Msftp",
			Width:  width,
			Height: height,
			Center: true,
		},
	})
	if w == nil {
		log.Print("WebView2 runtime is unavailable")
		return
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	makeFrameless(hwnd)
	_ = w.Bind("windowDrag", func() error {
		startWindowDrag(hwnd)
		return nil
	})
	_ = w.Bind("windowMinimize", func() error {
		showWindow(hwnd, swMinimize)
		return nil
	})
	_ = w.Bind("windowToggleMaximize", func() error {
		toggleMaximize(hwnd)
		return nil
	})
	_ = w.Bind("windowClose", func() error {
		destroyWindow(hwnd)
		w.Terminate()
		return nil
	})
	w.Navigate(target)
	w.Run()
}

func initialWindowSize() (uint, uint) {
	screenWidth, _, _ := procGetSystemMetrics.Call(0)
	screenHeight, _, _ := procGetSystemMetrics.Call(1)
	if screenWidth == 0 || screenHeight == 0 {
		return 1440, 820
	}
	return uint(screenWidth * 4 / 5), uint(screenHeight * 4 / 5)
}

func makeFrameless(hwnd uintptr) {
	if runtime.GOOS != "windows" {
		return
	}
	style, _, _ := procGetWindowLongPtr.Call(hwnd, gwlStyle)
	style &^= uintptr(wsCaption)
	style |= uintptr(wsSysMenu | wsMinimizeBox | wsMaximizeBox | wsThickFrame)
	procSetWindowLongPtr.Call(hwnd, gwlStyle, style)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swPNoMove|swPNoSize|swPNoZOrder|swPFrameChged)
}

func startWindowDrag(hwnd uintptr) {
	if windowMaximized {
		return
	}
	procReleaseCapture.Call()
	procSendMessage.Call(hwnd, wmNCLButtonDown, htCaption, 0)
}

func restoreMaximizedWindowForDrag(hwnd uintptr) {
	setResizableFrame(hwnd, true)
	restore := windowRestore
	width := restore.Right - restore.Left
	height := restore.Bottom - restore.Top
	if width <= 0 || height <= 0 {
		width, height = 1200, 760
	}
	var cursor windowPoint
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok != 0 {
		left := cursor.X - width/2
		if left < 0 {
			left = 0
		}
		restore = windowRect{Left: left, Top: cursor.Y - 12, Right: left + width, Bottom: cursor.Y - 12 + height}
	}
	setWindowRect(hwnd, restore)
	windowMaximized = false
}

func showWindow(hwnd uintptr, command int) {
	procShowWindow.Call(hwnd, uintptr(command))
}

func toggleMaximize(hwnd uintptr) {
	if windowMaximized {
		setResizableFrame(hwnd, true)
		setWindowRect(hwnd, windowRestore)
		windowMaximized = false
		return
	}
	if !getWindowRect(hwnd, &windowRestore) {
		return
	}
	work, ok := monitorWorkArea(hwnd)
	if !ok {
		return
	}
	setResizableFrame(hwnd, false)
	setWindowRect(hwnd, work)
	windowMaximized = true
}

func getWindowRect(hwnd uintptr, rect *windowRect) bool {
	ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(rect)))
	return ok != 0
}

func setResizableFrame(hwnd uintptr, enabled bool) {
	style, _, _ := procGetWindowLongPtr.Call(hwnd, gwlStyle)
	if enabled {
		style |= uintptr(wsThickFrame)
	} else {
		style &^= uintptr(wsThickFrame)
	}
	procSetWindowLongPtr.Call(hwnd, gwlStyle, style)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swPNoMove|swPNoSize|swPNoZOrder|swPFrameChged)
}

func setWindowRect(hwnd uintptr, rect windowRect) {
	procSetWindowPos.Call(hwnd, 0, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), swPNoZOrder|swPShowWindow|swPFrameChged)
}

func monitorWorkArea(hwnd uintptr) (windowRect, bool) {
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if monitor == 0 {
		return windowRect{}, false
	}
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, _ := procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return windowRect{}, false
	}
	return info.Work, true
}

func destroyWindow(hwnd uintptr) {
	procDestroyWindow.Call(hwnd)
}

func webviewDataDir() string {
	dir := filepath.Join(sharedDataDir(), "WebView2Profile-msftp-v3")
	_ = ensureWritableDir(dir)
	return dir
}

func sharedDataDir() string {
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exePath), "data")
		if ensureWritableDir(dir) == nil {
			return dir
		}
	}

	base := os.Getenv("LocalAppData")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "mshell", "data")
	_ = ensureWritableDir(dir)
	return dir
}

func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func init() {
	if runtime.GOOS != "windows" {
		panic(errors.New("msftp desktop build is intended for Windows"))
	}
}
