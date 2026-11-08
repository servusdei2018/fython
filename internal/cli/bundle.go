package cli

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Magic constants for the standalone binary trailer.
const (
	BundleTrailerMagic = "FYTHBNDL"
	BundleFooterSize   = 16
	VirtualBundleRoot  = "<bundled_app>"
)

// BundleManifest describes the embedded payload inside a standalone binary.
type BundleManifest struct {
	Entry       string   `json:"entry"`
	IsPackage   bool     `json:"is_package"`
	EntryModule string   `json:"entry_module,omitempty"`
	Optimize    int      `json:"optimize"`
	Wheels      []string `json:"wheels,omitempty"`
}

// BuildOptions contains options for constructing a standalone native executable.
type BuildOptions struct {
	Entry      string
	Output     string
	Wheels     []string
	Includes   []string
	Optimize   int
	BaseFython string // Optional path to base fython executable (defaults to running binary)
}

// DetectBundleTrailer checks whether r contains the "FYTHBNDL" trailer.
func DetectBundleTrailer(r io.ReaderAt, size int64) (uint64, bool, error) {
	if size < BundleFooterSize {
		return 0, false, nil
	}
	footer := make([]byte, BundleFooterSize)
	if _, err := r.ReadAt(footer, size-BundleFooterSize); err != nil {
		return 0, false, err
	}
	if string(footer[8:]) != BundleTrailerMagic {
		return 0, false, nil
	}
	offset := binary.LittleEndian.Uint64(footer[:8])
	if offset >= uint64(size-BundleFooterSize) {
		return 0, false, fmt.Errorf("corrupt bundle: invalid offset %d (file size %d)", offset, size)
	}
	return offset, true, nil
}

// IsBundledExecutable reports whether the current process binary contains an embedded bundle.
func IsBundledExecutable() bool {
	exePath, err := os.Executable()
	if err != nil {
		return false
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return false
	}
	f, err := os.Open(exePath)
	if err != nil {
		return false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return false
	}
	_, ok, _ := DetectBundleTrailer(f, fi.Size())
	return ok
}

// RunBundledExecutable mounts and runs the embedded application bundle.
func RunBundledExecutable() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return err
	}
	f, err := os.Open(exePath)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	bundleOffset, ok, err := DetectBundleTrailer(f, fi.Size())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("not a bundled executable")
	}

	payloadSize := fi.Size() - BundleFooterSize - int64(bundleOffset)
	payloadReader := io.NewSectionReader(f, int64(bundleOffset), payloadSize)
	payloadBytes, err := io.ReadAll(payloadReader)
	if err != nil {
		return fmt.Errorf("failed to read bundle payload: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(payloadBytes), payloadSize)
	if err != nil {
		return fmt.Errorf("failed to open bundle zip archive: %w", err)
	}

	// 1. Read manifest
	manifestFile, err := zr.Open("__bundle_manifest__.json")
	if err != nil {
		return fmt.Errorf("bundle missing manifest: %w", err)
	}
	var manifest BundleManifest
	if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		_ = manifestFile.Close()
		return fmt.Errorf("corrupt bundle manifest: %w", err)
	}
	_ = manifestFile.Close()

	// 2. Mount bundle zip in-memory
	if _, err := vm.RegisterVirtualZipArchive(VirtualBundleRoot, payloadBytes); err != nil {
		return fmt.Errorf("failed to mount bundle root: %w", err)
	}

	// 3. Mount embedded wheels in-memory
	var wheelPaths []string
	for _, wheelRel := range manifest.Wheels {
		wf, err := zr.Open(wheelRel)
		if err != nil {
			return fmt.Errorf("failed to open embedded wheel %s: %w", wheelRel, err)
		}
		rawWheel, err := io.ReadAll(wf)
		_ = wf.Close()
		if err != nil {
			return fmt.Errorf("failed to read embedded wheel %s: %w", wheelRel, err)
		}
		virtPath := VirtualBundleRoot + "/" + wheelRel
		if _, err := vm.RegisterVirtualZipArchive(virtPath, rawWheel); err != nil {
			return fmt.Errorf("failed to mount embedded wheel %s: %w", wheelRel, err)
		}
		wheelPaths = append(wheelPaths, virtPath)
	}

	// 4. Set up engine and runtime config
	argv := append([]string{manifest.Entry}, os.Args[1:]...)
	rtCfg := vm.RuntimeConfig{
		Argv:     argv,
		OrigArgv: append([]string(nil), os.Args...),
		Optimize: manifest.Optimize,
	}

	prevOpt := compiler.DefaultOptimizeLevel()
	compiler.SetDefaultOptimizeLevel(manifest.Optimize)
	defer compiler.SetDefaultOptimizeLevel(prevOpt)

	eng := vm.DefaultEngine()
	prevCfg := eng.RuntimeConfig()
	eng.SetRuntimeConfig(rtCfg)
	defer eng.SetRuntimeConfig(prevCfg)

	v := eng.NewVM()

	// Prepend virtual bundle root and wheels to sys.path
	sysModVal, ok := v.GetModule("sys")
	if ok && sysModVal.IsModule() {
		if pathVal, ok := sysModVal.AsModule().Dict.Get(val.InternString("path")); ok && pathVal.IsList() {
			list := pathVal.AsList()
			var prepend []val.Value
			prepend = append(prepend, val.NewStringVal(VirtualBundleRoot))
			for _, wp := range wheelPaths {
				prepend = append(prepend, val.NewStringVal(wp))
			}
			list.Items = append(prepend, list.Items...)
		}
	}

	// 5. Execute entry point
	if manifest.IsPackage {
		stmt := "import runpy\nrunpy._run_module_as_main('__main__', False)\n"
		co, err := compiler.CompileSourceModeWithOptimize([]byte(stmt), "<runpy>", compiler.ModeExec, manifest.Optimize)
		if err != nil {
			return err
		}
		v.RunCode(co)
		return unhandledError(v)
	}

	entryFile, err := zr.Open(manifest.Entry)
	if err != nil {
		return fmt.Errorf("failed to open bundled entry file %s: %w", manifest.Entry, err)
	}
	src, err := io.ReadAll(entryFile)
	_ = entryFile.Close()
	if err != nil {
		return fmt.Errorf("failed to read bundled entry file %s: %w", manifest.Entry, err)
	}

	co, err := compiler.CompileSourceModeWithOptimize(src, manifest.Entry, compiler.ModeExec, manifest.Optimize)
	if err != nil {
		return err
	}
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.SetGlobal("__file__", val.NewStringVal(manifest.Entry))
	v.RunCode(co)
	return unhandledError(v)
}

// BuildStandaloneBinary builds a self-contained zero-toolchain executable from opts.
func BuildStandaloneBinary(opts BuildOptions) error {
	basePath := opts.BaseFython
	if basePath == "" {
		basePath = os.Getenv("FYTHON_EXECUTABLE")
	}
	if basePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to resolve current executable: %w", err)
		}
		basePath = exe
	}
	basePath, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks for %s: %w", basePath, err)
	}

	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("failed to read base fython executable %s: %w", basePath, err)
	}

	// If the base binary is already bundled, strip the trailer and payload to recover the pristine base.
	if len(baseBytes) >= BundleFooterSize && string(baseBytes[len(baseBytes)-8:]) == BundleTrailerMagic {
		oldOffset := binary.LittleEndian.Uint64(baseBytes[len(baseBytes)-16 : len(baseBytes)-8])
		if oldOffset < uint64(len(baseBytes)-BundleFooterSize) {
			baseBytes = baseBytes[:oldOffset]
		}
	}

	// Validate entry target
	entryStat, err := os.Stat(opts.Entry)
	if err != nil {
		return fmt.Errorf("can't stat entry %s: %w", opts.Entry, err)
	}

	// Determine output path
	outputPath := opts.Output
	if outputPath == "" {
		base := filepath.Base(opts.Entry)
		outputPath = strings.TrimSuffix(base, ".py")
	}

	// Build in-memory ZIP bundle
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)

	manifest := BundleManifest{
		Optimize: opts.Optimize,
	}

	if entryStat.IsDir() {
		manifest.IsPackage = true
		manifest.Entry = "__main__.py"
		// Walk directory and add files
		err := filepath.Walk(opts.Entry, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, err := filepath.Rel(opts.Entry, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			w, err := zw.Create(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			_, err = w.Write(data)
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to bundle entry directory %s: %w", opts.Entry, err)
		}
	} else {
		manifest.IsPackage = false
		entryBase := filepath.Base(opts.Entry)
		manifest.Entry = entryBase
		data, err := os.ReadFile(opts.Entry)
		if err != nil {
			return fmt.Errorf("failed to read entry script %s: %w", opts.Entry, err)
		}
		w, err := zw.Create(entryBase)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}

		// Also bundle any sibling files in the entry's directory if imported
		entryDir := filepath.Dir(opts.Entry)
		if entryDir != "." && entryDir != "" {
			entries, _ := os.ReadDir(entryDir)
			for _, de := range entries {
				if de.IsDir() || de.Name() == entryBase || !strings.HasSuffix(de.Name(), ".py") {
					continue
				}
				siblingData, err := os.ReadFile(filepath.Join(entryDir, de.Name()))
				if err == nil {
					sw, err := zw.Create(de.Name())
					if err == nil {
						_, _ = sw.Write(siblingData)
					}
				}
			}
		}
	}

	// Process additional includes
	for _, inc := range opts.Includes {
		incStat, err := os.Stat(inc)
		if err != nil {
			return fmt.Errorf("can't include %s: %w", inc, err)
		}
		if incStat.IsDir() {
			incBase := filepath.Base(inc)
			err := filepath.Walk(inc, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				rel, err := filepath.Rel(inc, p)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				targetPath := filepath.ToSlash(filepath.Join(incBase, rel))
				w, err := zw.Create(targetPath)
				if err != nil {
					return err
				}
				_, err = w.Write(data)
				return err
			})
			if err != nil {
				return fmt.Errorf("failed to bundle include dir %s: %w", inc, err)
			}
		} else {
			data, err := os.ReadFile(inc)
			if err != nil {
				return fmt.Errorf("failed to read include file %s: %w", inc, err)
			}
			w, err := zw.Create(filepath.Base(inc))
			if err != nil {
				return err
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
		}
	}

	// Process wheels
	for _, whl := range opts.Wheels {
		whlData, err := os.ReadFile(whl)
		if err != nil {
			return fmt.Errorf("failed to read wheel %s: %w", whl, err)
		}
		whlBase := filepath.Base(whl)
		whlRel := "wheels/" + whlBase
		w, err := zw.Create(whlRel)
		if err != nil {
			return err
		}
		if _, err := w.Write(whlData); err != nil {
			return err
		}
		manifest.Wheels = append(manifest.Wheels, whlRel)
	}

	// Write manifest to zip
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	mw, err := zw.Create("__bundle_manifest__.json")
	if err != nil {
		return err
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		return err
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("failed to finalize zip archive: %w", err)
	}

	// Assemble standalone executable:
	// [Base Binary Bytes] + [Zip Bytes] + [8-byte BundleOffset LE] + [8-byte "FYTHBNDL"]
	out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create output executable %s: %w", outputPath, err)
	}
	defer out.Close()

	if _, err := out.Write(baseBytes); err != nil {
		return fmt.Errorf("failed to write base binary: %w", err)
	}

	bundleOffset := uint64(len(baseBytes))
	zipBytes := zipBuf.Bytes()
	if _, err := out.Write(zipBytes); err != nil {
		return fmt.Errorf("failed to write bundle payload: %w", err)
	}

	footer := make([]byte, BundleFooterSize)
	binary.LittleEndian.PutUint64(footer[:8], bundleOffset)
	copy(footer[8:], BundleTrailerMagic)
	if _, err := out.Write(footer); err != nil {
		return fmt.Errorf("failed to write bundle trailer: %w", err)
	}

	_ = out.Sync()
	return nil
}
