package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// resolveExtensionPath materializes .so/.pyd entries that live inside a
// registered zip/wheel archive to a dlopen-able file path.
// It reports ok=false when filePath is already a plain disk path.
func resolveExtensionPath(filePath string) (string, bool, error) {
	if !isExtensionFile(filePath) {
		return filePath, false, nil
	}
	_, innerPath, arch, ok := splitZipArchivePath(filePath)
	if !ok || arch == nil || innerPath == "" {
		return filePath, false, nil
	}
	data, found := arch.readEntry(innerPath)
	if !found {
		return filePath, false, nil
	}
	return materializeExtensionBytes(data, filepath.Base(innerPath))
}

// materializeExtensionBytes writes data to a content-addressed cache location
// and returns a path suitable for dlopen. It preserves the original soname
// basename inside the hash directory so DT_SONAME / relro checks work.
func materializeExtensionBytes(data []byte, soname string) (string, bool, error) {
	if soname == "" {
		soname = "extension.so"
	}
	// Linux diskless path when requested (memfd stays open for module lifetime).
	if runtime.GOOS == "linux" && os.Getenv("FYTHON_EXT_USE_MEMFD") == "1" {
		if p, err := materializeExtensionMemfd(data, soname); err == nil {
			return p, true, nil
		}
		// Fall through to disk cache on memfd failure.
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(cacheRoot, "fython", "ext", hash)
	dst := filepath.Join(dir, soname)
	if info, err := os.Stat(dst); err == nil && !info.IsDir() {
		if info.Size() == int64(len(data)) {
			return dst, true, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(dir, soname+".tmp-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", false, err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		// Concurrent writer won the race; prefer the existing file.
		if _, serr := os.Stat(dst); serr == nil {
			return dst, true, nil
		}
		return "", false, err
	}
	return dst, true, nil
}

var memfdKeepMu sync.Mutex
var memfdKeep []*os.File

// materializeExtensionMemfd creates an anonymous memfd containing data and
// returns /proc/self/fd/<fd>. The fd is kept open for the lifetime of the
// loaded module so lazy symbol resolution still resolves the inode.
func materializeExtensionMemfd(data []byte, soname string) (string, error) {
	fd, err := memfdCreate(soname)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), "fython-memfd-"+soname)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	// Rewind so dlopen can mmap from offset 0.
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return "", err
	}
	memfdKeepMu.Lock()
	memfdKeep = append(memfdKeep, f)
	memfdKeepMu.Unlock()
	return fmt.Sprintf("/proc/self/fd/%d", fd), nil
}
