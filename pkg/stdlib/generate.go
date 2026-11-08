//go:build ignore

// Command generate refreshes the embedded CPython library drop.
//
//	go generate ./pkg/stdlib/
//
// It downloads the pinned CPython tag, writes lib/LICENSE verbatim,
// and extracts the manifest of Lib/ modules verbatim. There are no overlays:
// every .py file under lib/ is byte-for-byte upstream, which lib/UPSTREAM.sha256
// records (and pkg/stdlib's tests enforce).
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const cpythonTag = "v3.15.0"

// upstream is the set of Lib/ paths copied from the pinned tag.
var upstream = []string{
	"__future__.py",
	"_ast_unparse.py",
	"_collections_abc.py",
	"_py_abc.py",
	"_py_warnings.py",
	"_weakrefset.py",
	"abc.py",
	"annotationlib.py",
	"ast.py",
	"argparse.py",
	"base64.py",
	"bdb.py",
	"bisect.py",
	"calendar.py",
	"code.py",
	"collections/__init__.py",
	"compression/__init__.py",
	"compression/_common/__init__.py",
	"compression/_common/_streams.py",
	"contextlib.py",
	"copy.py",
	"copyreg.py",
	"csv.py",
	"dataclasses.py",
	"datetime.py",
	"difflib.py",
	"doctest.py",
	"enum.py",
	"fnmatch.py",
	"functools.py",
	"genericpath.py",
	"getpass.py",
	"glob.py",
	"gzip.py",
	"hashlib.py",
	"heapq.py",
	"hmac.py",
	"ipaddress.py",
	"keyword.py",
	"linecache.py",
	"locale.py",
	"ntpath.py",
	"pathlib/__init__.py",
	"pathlib/_local.py",
	"pathlib/_os.py",
	"pathlib/types.py",
	"posixpath.py",
	"pprint.py",
	"random.py",
	"reprlib.py",
	"secrets.py",
	"shutil.py",
	"signal.py",
	"stat.py",
	"struct.py",
	"tempfile.py",
	"textwrap.py",
	"token.py",
	"tokenize.py",
	"tracemalloc.py",
	"typing.py",
	"urllib/__init__.py",
	"urllib/parse.py",
	"uuid.py",
	"warnings.py",
	"zipfile/__init__.py",
	"zipfile/__main__.py",
	"zipfile/_path/__init__.py",
	"zipfile/_path/_functools.py",
	"zipfile/_path/glob.py",
	"unittest/__init__.py",
	"unittest/__main__.py",
	"unittest/_log.py",
	"unittest/async_case.py",
	"unittest/case.py",
	"unittest/loader.py",
	"unittest/main.py",
	"unittest/mock.py",
	"unittest/result.py",
	"unittest/runner.py",
	"unittest/signals.py",
	"unittest/suite.py",
	"unittest/util.py",
	// Network stack
	"socket.py",
	"ssl.py",
	"selectors.py",
	"socketserver.py",
	// HTTP & urllib stack
	"http/__init__.py",
	"http/client.py",
	"http/server.py",
	"http/cookies.py",
	"http/cookiejar.py",
	"urllib/error.py",
	"urllib/request.py",
	"urllib/response.py",
	"urllib/robotparser.py",
	// Email stack
	"email/__init__.py",
	"email/_encoded_words.py",
	"email/_header_value_parser.py",
	"email/_parseaddr.py",
	"email/_policybase.py",
	"email/base64mime.py",
	"email/charset.py",
	"email/contentmanager.py",
	"email/encoders.py",
	"email/errors.py",
	"email/feedparser.py",
	"email/generator.py",
	"email/header.py",
	"email/headerregistry.py",
	"email/iterators.py",
	"email/message.py",
	"email/parser.py",
	"email/policy.py",
	"email/quoprimime.py",
	"email/utils.py",
	"email/mime/__init__.py",
	"email/mime/application.py",
	"email/mime/audio.py",
	"email/mime/base.py",
	"email/mime/image.py",
	"email/mime/message.py",
	"email/mime/multipart.py",
	"email/mime/nonmultipart.py",
	"email/mime/text.py",
	// HTML, mimetypes, quopri, string, codecs, contextvars
	"html/__init__.py",
	"html/entities.py",
	"mimetypes.py",
	"quopri.py",
	"string/__init__.py",
	"string/templatelib.py",
	"codecs.py",
	"contextvars.py",
	// Subprocess stack
	"subprocess.py",
	// Exact numerics stack
	"numbers.py",
	"fractions.py",
	"_pydecimal.py",
	"decimal.py",
	// SQLite database stack
	"sqlite3/__init__.py",
	"sqlite3/dbapi2.py",
	"sqlite3/dump.py",
	// Ecosystem battery
	"logging/__init__.py",
	"logging/handlers.py",
	"logging/config.py",
	"tomllib/__init__.py",
	"tomllib/_parser.py",
	"tomllib/_re.py",
	"tomllib/_types.py",
	"configparser.py",
	"tarfile.py",
	"queue.py",
	"concurrent/__init__.py",
	"concurrent/futures/__init__.py",
	"concurrent/futures/_base.py",
	"concurrent/futures/thread.py",
	"concurrent/futures/process.py",
	"pkgutil.py",
	// Module execution, packaging, metadata & resources
	"runpy.py",
	"zipimport.py",
	"inspect.py",
	"importlib/__init__.py",
	"importlib/_abc.py",
	"importlib/abc.py",
	"importlib/machinery.py",
	"importlib/util.py",
	"importlib/readers.py",
	"importlib/simple.py",
	"importlib/metadata/__init__.py",
	"importlib/metadata/_adapters.py",
	"importlib/metadata/_collections.py",
	"importlib/metadata/_context.py",
	"importlib/metadata/_functools.py",
	"importlib/metadata/_itertools.py",
	"importlib/metadata/_meta.py",
	"importlib/metadata/_text.py",
	"importlib/metadata/_typing.py",
	"importlib/metadata/diagnose.py",
	"importlib/resources/__init__.py",
	"importlib/resources/_adapters.py",
	"importlib/resources/_common.py",
	"importlib/resources/_functional.py",
	"importlib/resources/_itertools.py",
	"importlib/resources/abc.py",
	"importlib/resources/readers.py",
	"importlib/resources/simple.py",
	"json/__init__.py",
	"json/__main__.py",
	"json/tool.py",
	"json/decoder.py",
	"json/encoder.py",
	"json/scanner.py",
	// Platform and sysconfig
	"platform.py",
	"sysconfig/__init__.py",
	"sysconfig/__main__.py",
	// Shell and colors
	"shlex.py",
	"colorsys.py",
	// Pickle
	"_compat_pickle.py",
	"pickle.py",
	// XML
	"xml/__init__.py",
	"xml/utils.py",
	"xml/etree/__init__.py",
	"xml/etree/ElementTree.py",
	"xml/etree/ElementPath.py",
}

func main() {
	localLib := os.Getenv("CPYTHON_LIB_DIR")
	if localLib == "" {
		if fi, err := os.Stat("/usr/lib64/python3.15"); err == nil && fi.IsDir() {
			localLib = "/usr/lib64/python3.15"
		}
	}

	if localLib != "" {
		for _, rel := range upstream {
			src := path.Join(localLib, rel)
			dest := path.Join("lib", rel)
			data, err := os.ReadFile(src)
			if err != nil {
				fatalf("read local %s: %v", src, err)
			}
			if err := os.MkdirAll(path.Dir(dest), 0o755); err != nil {
				fatalf("mkdir %s: %v", path.Dir(dest), err)
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				fatalf("write %s: %v", dest, err)
			}
		}
		license, err := os.ReadFile("lib/LICENSE")
		if err != nil {
			fatalf("read license: %v", err)
		}
		if !strings.Contains(string(license), "PYTHON SOFTWARE FOUNDATION LICENSE VERSION 2") {
			fatalf("lib/LICENSE is not the PSF License Version 2")
		}
		if err := writeManifest(); err != nil {
			fatalf("manifest: %v", err)
		}
		fmt.Printf("wrote lib/LICENSE and %d modules from local CPython %s\n", len(upstream), cpythonTag)
		return
	}

	url := "https://github.com/python/cpython/archive/refs/tags/" + cpythonTag + ".tar.gz"
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		fatalf("download %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fatalf("download %s: %s", url, resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		fatalf("gzip: %v", err)
	}
	defer gz.Close()

	want := map[string]string{"LICENSE": "lib/LICENSE"}
	for _, rel := range upstream {
		want["Lib/"+rel] = path.Join("lib", rel)
	}
	found := map[string]bool{}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fatalf("tar: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := stripRoot(hdr.Name)
		dest, ok := want[rel]
		if !ok {
			continue
		}
		if err := writeFile(dest, tr); err != nil {
			fatalf("write %s: %v", dest, err)
		}
		found[rel] = true
	}

	for rel := range want {
		if !found[rel] {
			fatalf("tarball %s missing %s", cpythonTag, rel)
		}
	}

	license, err := os.ReadFile("lib/LICENSE")
	if err != nil {
		fatalf("read license: %v", err)
	}
	if !strings.Contains(string(license), "PYTHON SOFTWARE FOUNDATION LICENSE VERSION 2") {
		fatalf("lib/LICENSE is not the PSF License Version 2")
	}
	if err := writeManifest(); err != nil {
		fatalf("manifest: %v", err)
	}
	fmt.Printf("wrote lib/LICENSE and %d modules from CPython %s\n", len(upstream), cpythonTag)
}

func stripRoot(name string) string {
	name = path.Clean(strings.TrimPrefix(name, "./"))
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func writeFile(dest string, r io.Reader) error {
	if err := os.MkdirAll(path.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "generate: "+format+"\n", args...)
	os.Exit(1)
}

// writeManifest records the sha256 of every file under lib/ (except itself) so
// tests can prove nothing under lib/ diverges from the pinned CPython tag.
func writeManifest() error {
	var lines []string
	err := filepathWalk("lib", func(p string) error {
		if p == "lib/UPSTREAM.sha256" || strings.Contains(p, "__pycache__") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("%x  %s", sum, strings.TrimPrefix(p, "lib/")))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(lines, func(i, j int) bool {
		return lines[i][66:] < lines[j][66:]
	})
	header := "# sha256 of every file under lib/, copied verbatim from CPython " + cpythonTag + ".\n" +
		"# Generated by generate.go; do not edit. There are no overlays.\n"
	return os.WriteFile("lib/UPSTREAM.sha256", []byte(header+strings.Join(lines, "\n")+"\n"), 0o644)
}

func filepathWalk(root string, fn func(p string) error) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := root + "/" + e.Name()
		if e.IsDir() {
			if err := filepathWalk(p, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}
