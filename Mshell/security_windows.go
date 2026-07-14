//go:build windows
// +build windows

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	dpapiPasswordPrefix     = "dpapi:v1:"
	cryptProtectUIForbidden = 0x1
	messageBoxYesNo         = 0x00000004
	messageBoxIconWarning   = 0x00000030
	messageBoxDefaultNo     = 0x00000100
	messageBoxResultYes     = 6
)

type dataBlob struct {
	dataLen uint32
	data    *byte
}

var (
	crypt32                   = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData      = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData    = crypt32.NewProc("CryptUnprotectData")
	procLocalFreeForSecurity  = kernel32.NewProc("LocalFree")
	procMessageBoxForSecurity = user32.NewProc("MessageBoxW")
	knownHostsMu              sync.Mutex
)

func protectPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	plain := []byte(password)
	in := dataBlob{dataLen: uint32(len(plain)), data: &plain[0]}
	var out dataBlob
	result, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return "", fmt.Errorf("encrypt password with Windows DPAPI: %w", callErr)
	}
	defer procLocalFreeForSecurity.Call(uintptr(unsafe.Pointer(out.data)))
	ciphertext := unsafe.Slice(out.data, int(out.dataLen))
	return dpapiPasswordPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

func unprotectPassword(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, dpapiPasswordPrefix) {
		return value, nil // Legacy plaintext is detected and migrated by loadSavedAccounts.
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, dpapiPasswordPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted password: %w", err)
	}
	if len(ciphertext) == 0 {
		return "", errors.New("encrypted password is empty")
	}
	in := dataBlob{dataLen: uint32(len(ciphertext)), data: &ciphertext[0]}
	var out dataBlob
	result, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return "", fmt.Errorf("decrypt password with Windows DPAPI: %w", callErr)
	}
	defer procLocalFreeForSecurity.Call(uintptr(unsafe.Pointer(out.data)))
	plaintext := unsafe.Slice(out.data, int(out.dataLen))
	return string(plaintext), nil
}

func decryptSavedAccountPasswords(accounts []savedAccount) (bool, error) {
	migrationNeeded := false
	for i := range accounts {
		if accounts[i].Password != "" && !strings.HasPrefix(accounts[i].Password, dpapiPasswordPrefix) {
			migrationNeeded = true
		}
		password, err := unprotectPassword(accounts[i].Password)
		if err != nil {
			return false, fmt.Errorf("decrypt password for account %q: %w", accountSortName(accounts[i]), err)
		}
		accounts[i].Password = password
	}
	return migrationNeeded, nil
}

func encryptSavedAccountPasswords(accounts []savedAccount) ([]savedAccount, error) {
	encrypted := append([]savedAccount(nil), accounts...)
	for i := range encrypted {
		password, err := protectPassword(encrypted[i].Password)
		if err != nil {
			return nil, fmt.Errorf("encrypt password for account %q: %w", accountSortName(encrypted[i]), err)
		}
		encrypted[i].Password = password
	}
	return encrypted, nil
}

func verifiedHostKeyCallback() (ssh.HostKeyCallback, error) {
	path := filepath.Join(filepath.Dir(accountsPath()), "known_hosts")
	if err := ensureWritableDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SSH known_hosts: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close SSH known_hosts: %w", err)
	}

	knownHostsFiles := []string{path}
	if userHome, err := os.UserHomeDir(); err == nil {
		openSSHKnownHosts := filepath.Join(userHome, ".ssh", "known_hosts")
		if info, statErr := os.Stat(openSSHKnownHosts); statErr == nil && !info.IsDir() {
			knownHostsFiles = append(knownHostsFiles, openSSHKnownHosts)
		}
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		check, err := knownhosts.New(knownHostsFiles...)
		if err != nil {
			return fmt.Errorf("load SSH known_hosts: %w", err)
		}
		if err := check(hostname, remote, key); err == nil {
			return nil
		} else {
			keyErr, ok := err.(*knownhosts.KeyError)
			if !ok {
				return fmt.Errorf("verify SSH host key: %w", err)
			}
			if len(keyErr.Want) > 0 {
				return fmt.Errorf("SSH host key changed for %s; received %s", hostname, ssh.FingerprintSHA256(key))
			}
		}

		if !confirmUnknownHostKey(hostname, ssh.FingerprintSHA256(key)) {
			return fmt.Errorf("SSH host key for %s was not trusted", hostname)
		}

		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("save SSH host key: %w", err)
		}
		defer file.Close()
		if _, err := file.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("save SSH host key: %w", err)
		}
		return nil
	}, nil
}

func confirmUnknownHostKey(hostname, fingerprint string) bool {
	title, err := syscall.UTF16PtrFromString("SSH server identity verification")
	if err != nil {
		return false
	}
	message, err := syscall.UTF16PtrFromString(fmt.Sprintf(
		"This is the first connection to %s.\n\nServer key fingerprint:\n%s\n\nTrust this server and save its key?",
		hostname,
		fingerprint,
	))
	if err != nil {
		return false
	}
	result, _, _ := procMessageBoxForSecurity.Call(
		0,
		uintptr(unsafe.Pointer(message)),
		uintptr(unsafe.Pointer(title)),
		messageBoxYesNo|messageBoxIconWarning|messageBoxDefaultNo,
	)
	return result == messageBoxResultYes
}
