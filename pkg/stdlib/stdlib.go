package stdlib

import (
	"embed"
	"io/fs"
	"path"
	"strings"
	"sync"
)

//go:generate go run generate.go

//go:embed all:lib
var StdlibFS embed.FS

var (
	cpythonLicenseOnce sync.Once
	cpythonLicenseData []byte
)

// CPythonLicense returns the PSF license shipped with the pinned CPython drop.
// It lives in the embedded tree as lib/LICENSE so the build does not depend
// on a second //go:embed pattern, and is loaded lazily on first use.
func CPythonLicense() []byte {
	cpythonLicenseOnce.Do(func() {
		b, err := StdlibFS.ReadFile("lib/LICENSE")
		if err != nil {
			panic("stdlib: embedded CPython license missing: " + err.Error())
		}
		cpythonLicenseData = b
	})
	return cpythonLicenseData
}

// FindModule locates a pure-Python module in the embedded standard library.
// Returns (relativePath, isPackage, found).
func FindModule(moduleName string) (string, bool, bool) {
	parts := strings.Split(moduleName, ".")
	relPath := path.Join(parts...)

	// 1. Directory package: lib/pkg/__init__.py
	initPath := path.Join("lib", relPath, "__init__.py")
	if _, err := fs.Stat(StdlibFS, initPath); err == nil {
		return initPath, true, true
	}

	// 2. Single file: lib/mod.py
	modPath := path.Join("lib", relPath+".py")
	if _, err := fs.Stat(StdlibFS, modPath); err == nil {
		return modPath, false, true
	}

	return "", false, false
}

// ReadModule reads the source code of an embedded module.
func ReadModule(vfsPath string) ([]byte, error) {
	return StdlibFS.ReadFile(vfsPath)
}
