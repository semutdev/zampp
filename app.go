package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// homeDir returns the user's absolute home directory path.
func homeDir() (string, error) {
	// Use os.UserHomeDir() instead of user.Current().HomeDir so the path is
	// resolved via $HOME (prefers the same value the user sees in a shell),
	// and so we don't pull in os/user's cgo path on platforms where it can
	// be avoided. Apache does not understand `~`, so the absolute path
	// returned here is what gets written into httpd.conf.
	return os.UserHomeDir()
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Ensure the standalone directory structure exists on first run.
	if err := EnsureAppDirs(); err != nil {
		fmt.Println("startup: failed to ensure app dirs:", err)
	}
	// Pre-generate web server configs so they're ready before Start is clicked.
	if err := GenerateNginxConfig(); err != nil {
		fmt.Println("startup: failed to generate nginx.conf:", err)
	}
	if err := GenerateApacheConfig(); err != nil {
		fmt.Println("startup: failed to generate httpd.conf:", err)
	}
	// Drop the default Welcome page into htdocs on first run.
	if err := GenerateDefaultIndexPHP(); err != nil {
		fmt.Println("startup: failed to generate default index.php:", err)
	}
}

// shutdown is called when the app is closing (normal close, Cmd+Q, or
// window close). It stops every engine ZAMPP started so that
// http://127.0.0.1:8000, the PHP built-in server, and MySQL do not keep
// running in the background after the app exits. Without this hook, a
// force-quit leaves orphan daemons bound to our ports.
func (a *App) shutdown(ctx context.Context) {
	a.stopAllEngines()
}

// stopAllEngines stops every ZAMPP engine (nginx, apache, php, mysql) using
// the multi-layer fallbacks already defined in StopNginx/etc. It is safe to
// call from any goroutine and is used by both OnShutdown and the signal
// handler in main() to cover force-quit / SIGTERM paths that bypass
// Wails' shutdown hook.
func (a *App) stopAllEngines() {
	_ = a.StopNginx()
	_ = a.StopApache()
	_ = a.StopPHP()
	_ = a.StopMySQL()
}

const appDirName = ".zampp"

// mySQLPort is the TCP port used by the standalone mysqld. It is set to 3307
// (instead of MySQL's default 3306) to avoid clashes with XAMPP's MySQL on 3306.
const mySQLPort = "3307"

// nginxPort is the public-facing port for Nginx.
const nginxPort = "8000"

// apachePort is the public-facing port for Apache. Now equal to nginxPort
// (8000) so the UI can treat them as a single "Web Server" slot — the user
// picks either engine via the dropdown, never both at once.
const apachePort = "8000"

// phpInternalPort is the internal PHP built-in server port that Nginx/Apache
// proxy PHP requests to. The UI does not need to know about this port.
const phpInternalPort = "8001"

// mysqlProcess holds a reference to the running mysqld process (if any).
// It is managed via StartMySQL / StopMySQL.
var mysqlProcess *exec.Cmd

// nginxProcess holds a reference to the running nginx process (if any).
// It is managed via StartNginx / StopNginx.
var nginxProcess *exec.Cmd

// apacheProcess holds a reference to the running httpd process (if any).
// It is managed via StartApache / StopApache.
var apacheProcess *exec.Cmd

// phpProcess holds a reference to the running PHP built-in server process
// (if any). It is managed via StartPHP / StopPHP.
var phpProcess *exec.Cmd

// safeBuffer is a goroutine-safe bytes.Buffer used to capture the first chunk
// of a child process's stderr so we can report it synchronously after a
// startup-timeout, while also teeing it to the parent stderr.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSafeBuffer() *safeBuffer { return &safeBuffer{} }

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}


// attachLogger wires a child process's stdout/stderr to a per-engine log
// file under ~/.zampp/logs/{engine}/, echoes them to the parent terminal so
// errors are visible in `wails dev`, and optionally tees stderr into an
// extra writer (e.g. a safeBuffer) so the caller can read the first chunk of
// startup errors synchronously after a port-listen timeout.
//
// IMPORTANT: callers MUST NOT also assign cmd.Stderr or call cmd.StderrPipe()
// after this — that double-wires stderr and causes a nil-pointer panic at
// cmd.Start() time, because StderrPipe() returns nil when Stderr != nil.
//
// Returns the log file handle; it must remain open for the lifetime of the
// child process, otherwise the child loses its stderr/stdout target.
func attachLogger(cmd *exec.Cmd, engineLogDir string, filename string, extraStderr io.Writer) (*os.File, error) {
	if err := os.MkdirAll(engineLogDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create log dir %s: %w", engineLogDir, err)
	}
	logPath := filepath.Join(engineLogDir, filename)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("cannot open log %s: %w", logPath, err)
	}

	stderrWriters := []io.Writer{f, os.Stderr}
	if extraStderr != nil {
		stderrWriters = append(stderrWriters, extraStderr)
	}

	// Go's exec package spawns its own internal goroutine to pump the child's
	// stdout/stderr into these writers. We never need to call StderrPipe()
	// ourselves (and must not — StderrPipe returns nil if Stderr != nil,
	// which then panics io.Copy).
	cmd.Stdout = io.MultiWriter(f, os.Stdout)
	cmd.Stderr = io.MultiWriter(stderrWriters...)
	return f, nil
}

// appRootDir returns the absolute path to the app's root directory
// inside the user's home directory (e.g. /Users/jamal/.zampp).
func appRootDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(u.HomeDir, appDirName), nil
}

// htdocsPath returns the absolute path to the htdocs directory.
func htdocsPath() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "htdocs"), nil
}

// phpBaseDir returns the absolute path under which per-version PHP binaries
// are stored (e.g. ~/.zampp/bin/php).
func phpBaseDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", "php"), nil
}

// CheckFirstRun reports whether the ZAMPP base engine bundle has been fully
// set up in ~/.zampp. Returns true only when every required BASE binary
// exists as a regular file:
//
//   - bin/apache/httpd
//   - bin/nginx/nginx
//   - bin/mysql/bin/mysqld
//
// PHP 7.4 is intentionally NOT checked here — it is part of the base
// engine bundle but, like every other PHP version, also has its own
// per-version Download button in the UI (driven by CheckPHPVersion).
// This separation means a missing 7.4 alone does not trigger a full
// re-download of the ~300 MB engine bundle; the user can just click the
// per-version Download button for 7.4 instead.
//
// The frontend uses this to decide whether to show the first-run
// setup/download overlay. Checking only the directory existence was too
// lax — a stale or partially-populated ~/.zampp would pass and the app
// would skip the engine download, leaving the user with no working
// servers.
func (a *App) CheckFirstRun() bool {
	root, err := appRootDir()
	if err != nil {
		return false
	}
	// Required base binaries for the engine to be considered "installed".
	// Mirrors the package-engine.sh validation set (excluding php/7.4).
	required := []string{
		filepath.Join(root, "bin", "apache", "httpd"),
		filepath.Join(root, "bin", "nginx", "nginx"),
		filepath.Join(root, "bin", "mysql", "bin", "mysqld"),
	}
	for _, p := range required {
		info, err := os.Stat(p)
		if err != nil {
			return false
		}
		if info.IsDir() {
			return false
		}
	}
	return true
}

// binariesZipURL is the GitHub release URL for the ZAMPP engine bundle
// (contains .zampp/bin and .zampp/conf at the archive root).
const binariesZipURL = "https://github.com/semutdev/zampp/releases/download/v1.0.0/zampp-mac-x64-v1.zip"

// DownloadAndExtractBinaries downloads the engine zip from GitHub releases,
// streaming 'download-progress' events (0-100) to the frontend, then
// extracts it into the user's home directory. Because the zip already
// contains the .zampp/ root folder, extraction lands at ~/.zampp/bin and
// ~/.zampp/conf. The temporary download at /tmp/zampp-engine.zip is
// deleted at the end, and a 'download-complete' event is emitted.
func (a *App) DownloadAndExtractBinaries() error {
	if a.ctx == nil {
		return fmt.Errorf("app context not initialized")
	}

	const tmpZip = "/tmp/zampp-engine.zip"

	// 1) Download with progress streaming.
	total, err := a.downloadWithProgress(binariesZipURL, tmpZip, "download-progress")
	if err != nil {
		return err
	}
	_ = total
	runtime.EventsEmit(a.ctx, "download-progress", 100)

	// 2) Extract zip into home directory (zip already contains .zampp/ root).
	home, err := homeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve home dir: %w", err)
	}
	if err := extractZipToDir(tmpZip, home); err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}

	// 3) Cleanup temp file.
	_ = os.Remove(tmpZip)

	// 4) Notify frontend.
	runtime.EventsEmit(a.ctx, "download-complete")
	return nil
}

// downloadWithProgress downloads url to destPath, emitting progress events
// (0-100) on the given eventName as bytes stream in. Returns the
// ContentLength (may be -1 if unknown).
func (a *App) downloadWithProgress(url, destPath, eventName string) (int64, error) {
	if a.ctx == nil {
		return -1, fmt.Errorf("app context not initialized")
	}

	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, url, nil)
	if err != nil {
		return -1, fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	total := resp.ContentLength
	fmt.Printf("[download] %s -> %s (ContentLength=%d)\n", url, destPath, total)
	out, err := os.Create(destPath)
	if err != nil {
		return total, fmt.Errorf("cannot create temp file %s: %w", destPath, err)
	}

	// Emit a "starting" 0% so the UI shows activity even before the first
	// full 1% chunk arrives (for a 529MB download, 1% = ~5MB which can take
	// a few seconds on slow connections — during which the bar would appear
	// stuck without this initial ping).
	runtime.EventsEmit(a.ctx, eventName, 0)
	fmt.Printf("[download] emit %s = 0%%\n", eventName)

	var downloaded int64
	buf := make([]byte, 32*1024)
	lastPct := 0
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return total, fmt.Errorf("write error: %w", werr)
			}
			downloaded += int64(n)
			if total > 0 {
				pct := int(float64(downloaded) / float64(total) * 100)
				if pct > lastPct && pct <= 100 {
					lastPct = pct
					runtime.EventsEmit(a.ctx, eventName, pct)
					// Log every 5% to terminal so we can confirm the Go side
					// is actually emitting events even if the frontend
					// listener is silent (helps diagnose Wails dev-mode
					// event delivery issues).
					if pct%5 == 0 {
						fmt.Printf("[download] emit %s = %d%% (%d / %d bytes)\n",
							eventName, pct, downloaded, total)
					}
				}
			} else {
				// Unknown total — emit a pseudo-progress based on bytes
				// downloaded so the UI shows something moving. We bend the
				// 0-100 contract here only while Content-Length is absent;
				// once we know the real total, we switch back.
				mbDownloaded := downloaded / (1024 * 1024)
				pseudo := int(mbDownloaded % 100)
				if pseudo != lastPct {
					lastPct = pseudo
					runtime.EventsEmit(a.ctx, eventName, pseudo)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return total, fmt.Errorf("download read error: %w", readErr)
		}
	}
	out.Close()
	fmt.Printf("[download] %s done (%d bytes)\n", url, downloaded)
	return total, nil
}

// CheckPHPVersion reports whether the given PHP version has been fully
// installed under ~/.zampp/bin/php/{version} with an executable `php`
// binary present. Returns true if installed, false otherwise. The frontend
// uses this to decide whether the selected version needs to be downloaded
// before starting the web server — including PHP 7.4, which is part of the
// base engine bundle but may still be missing on a fresh / stale install.
func (a *App) CheckPHPVersion(version string) bool {
	if version == "" {
		return false
	}
	_, err := getPHPExecutablePath(version)
	return err == nil
}

// phpVersionZipURL builds the GitHub Releases download URL for a per-version
// PHP bundle. The archive is expected to contain a top-level folder named
// {version} so that extraction into ~/.zampp/bin/php/ yields
// ~/.zampp/bin/php/{version}.
//
// The zip filename is platform-specific:
//   - darwin  -> php-{version}-mac.zip
//   - windows -> php-{version}-win.zip
//
// This allows shipping separate builds per OS while keeping the extracted
// folder layout ({version}/) identical across archives.
func phpVersionZipURL(version string) string {
	var osSuffix string
	switch goruntime.GOOS {
	case "darwin":
		osSuffix = "mac"
	case "windows":
		osSuffix = "win"
	default:
		// Unknown platform — fall back to the mac suffix so the URL is still
		// well-formed rather than producing "php-8.2-.zip".
		osSuffix = "mac"
	}
	return fmt.Sprintf("https://github.com/semutdev/zampp/releases/download/v1.0.0/php-%s-%s.zip", version, osSuffix)
}

// DownloadPHPVersion downloads and extracts the per-version PHP bundle for the
// given version (e.g. "8.2").
//
// Flow:
//  1. Download https://github.com/semutdev/zampp/releases/download/v1.0.0/php-{version}-{mac|win}.zip
//     to /tmp/php-{version}-{mac|win}.zip, emitting 'php-download-progress' (0-100).
//     The OS suffix is selected at runtime via goruntime.GOOS.
//  2. Extract into ~/.zampp/bin/php/ (zip already contains a top-level {version} folder).
//  3. Delete /tmp/php-{version}-{mac|win}.zip.
//  4. Emit 'php-download-complete'.
//
// The base engine (DownloadAndExtractBinaries) is independent and remains
// unchanged — it runs only on First Run.
func (a *App) DownloadPHPVersion(version string) error {
	if version == "" {
		return fmt.Errorf("version is empty")
	}
	if a.ctx == nil {
		return fmt.Errorf("app context not initialized")
	}

	base, err := phpBaseDir()
	if err != nil {
		return fmt.Errorf("cannot resolve php base dir: %w", err)
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return fmt.Errorf("cannot create php base dir %s: %w", base, err)
	}

	// Derive the platform-suffixed filename from the URL pattern so the
	// temp zip filename stays consistent with the remote asset name.
	var osSuffix string
	switch goruntime.GOOS {
	case "darwin":
		osSuffix = "mac"
	case "windows":
		osSuffix = "win"
	default:
		osSuffix = "mac"
	}
	tmpZip := fmt.Sprintf("/tmp/php-%s-%s.zip", version, osSuffix)
	url := phpVersionZipURL(version)

	// 1) Download with progress streaming.
	total, err := a.downloadWithProgress(url, tmpZip, "php-download-progress")
	if err != nil {
		return err
	}
	_ = total
	runtime.EventsEmit(a.ctx, "php-download-progress", 100)

	// 2) Extract into ~/.zampp/bin/php/.
	if err := extractZipToDir(tmpZip, base); err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}

	// 3) Cleanup temp file.
	_ = os.Remove(tmpZip)

	// 4) Notify frontend.
	runtime.EventsEmit(a.ctx, "php-download-complete")
	return nil
}

// extractZipToDir extracts all entries of the zip archive at zipPath into
// destDir, preserving the archive's internal directory structure.
func extractZipToDir(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("cannot open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Zip slip protection: ensure target stays within destDir.
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry outside destination dir: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("cannot create dir %s: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("cannot create parent dir for %s: %w", target, err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("cannot open zip entry %s: %w", f.Name, err)
		}

		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("cannot create %s: %w", target, err)
		}

		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return fmt.Errorf("cannot write %s: %w", target, err)
		}
		rc.Close()
		out.Close()
	}
	return nil
}

// getPHPExecutablePath resolves the PHP binary path for the given version,
// supporting two installation layouts:
//
//	Jalur A (SPC)  : ~/.zampp/bin/php/{version}/php
//	Jalur B (MAMP) : ~/.zampp/bin/php/{version}/bin/php
//
// It checks Jalur A first, then Jalur B. If a regular file is found at either
// location, its absolute path is returned. If neither exists, an error is
// returned describing the expected locations.
func getPHPExecutablePath(version string) (string, error) {
	base, err := phpBaseDir()
	if err != nil {
		return "", err
	}

	candidates := []string{
		filepath.Join(base, version, "php"),      // Jalur A (SPC)
		filepath.Join(base, version, "bin", "php"), // Jalur B (MAMP)
	}

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			return p, nil
		}
	}

	return "", fmt.Errorf(
		"Binary PHP %s belum terpasang di ~/.zampp/bin/php/%s/ (diperiksa: ./php dan ./bin/php)",
		version, version,
	)
}

// mysqlBaseDir returns the absolute path under which MySQL binaries are stored
// (e.g. ~/.zampp/bin/mysql).
func mysqlBaseDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", "mysql"), nil
}

// mysqlDataDir returns the absolute path to the MySQL data directory
// (e.g. ~/.zampp/data/mysql).
func mysqlDataDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "data", "mysql"), nil
}

// mysqldPath returns the absolute path to the mysqld binary
// (e.g. ~/.zampp/bin/mysql/bin/mysqld).
func mysqldPath() (string, error) {
	base, err := mysqlBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bin", "mysqld"), nil
}

// mysqlSocketPath returns the absolute path to the MySQL Unix socket file.
// It lives inside the data directory to avoid clashing with XAMPP's
// /tmp/mysql.sock.
func mysqlSocketPath() (string, error) {
	dataDir, err := mysqlDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "mysql.sock"), nil
}

// isMySQLInstalled reports whether the mysqld binary exists.
func isMySQLInstalled() bool {
	p, err := mysqldPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// EnsureAppDirs creates the standalone directory structure used by the app:
//
//	~/.zampp/
//	  ├── bin/php/         (per-version PHP binaries go here)
//	  ├── bin/mysql/       (MySQL binaries go here)
//	  ├── bin/nginx/       (Nginx binary goes here)
//	  ├── bin/apache/      (Apache httpd + modules go here)
//	  ├── conf/nginx/      (generated nginx.conf goes here)
//	  ├── conf/apache/     (generated httpd.conf goes here)
//	  ├── data/mysql/      (MySQL data directory)
//	  ├── logs/nginx/      (Nginx logs)
//	  ├── logs/apache/     (Apache logs)
//	  └── htdocs/          (document root)
func EnsureAppDirs() error {
	root, err := appRootDir()
	if err != nil {
		return err
	}
	dirs := []string{
		root,
		filepath.Join(root, "bin"),
		filepath.Join(root, "bin", "php"),
		filepath.Join(root, "bin", "mysql"),
		filepath.Join(root, "bin", "nginx"),
		filepath.Join(root, "bin", "apache"),
		filepath.Join(root, "conf"),
		filepath.Join(root, "conf", "nginx"),
		filepath.Join(root, "conf", "apache"),
		filepath.Join(root, "data"),
		filepath.Join(root, "data", "mysql"),
		filepath.Join(root, "logs"),
		filepath.Join(root, "logs", "nginx"),
		filepath.Join(root, "logs", "apache"),
		filepath.Join(root, "htdocs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("cannot create directory %s: %w", d, err)
		}
	}
	return nil
}

// isPHPInstalled reports whether the PHP binary exists for the given version.
func isPHPInstalled(version string) bool {
	_, err := getPHPExecutablePath(version)
	return err == nil
}

// phpVariantForVersion decides which PHP SAPI binary to launch for a given
// version and how to address it (FastCGI bind target).
//
//   - 7.4   → php-cgi -b 127.0.0.1:8001          (CGI, ships with base engine)
//   - 8.x+  → php-fpm --fpm-config <dynamic.conf> (FPM, modular downloads)
//
// php-fpm cannot bind to a port via a CLI flag — it must be configured through
// an FPM pool config file. writePHPFPMConfig generates a minimal config at
// ~/.zampp/config/php-fpm-{version}.conf with `listen = 127.0.0.1:8001`.
//
// Returns the resolved absolute binary path and the arg slice to pass to exec.
func phpLaunchCommand(version string) (binaryPath string, args []string, err error) {
	base, err := phpBaseDir()
	if err != nil {
		return "", nil, err
	}

	// Candidates: php-cgi / php-fpm may sit next to php (SPC layout) or
	// inside a bin/ subfolder (MAMP layout).
	lookup := func(name string) (string, bool) {
		for _, p := range []string{
			filepath.Join(base, version, name),
			filepath.Join(base, version, "bin", name),
		} {
			if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
				return p, true
			}
		}
		return "", false
	}

	isCGI := version == "7.4"

	var sapiName string
	if isCGI {
		sapiName = "php-cgi"
	} else {
		sapiName = "php-fpm"
	}

	bp, ok := lookup(sapiName)
	if !ok {
		// Construct a precise, actionable error. Start with "Error:" so
		// StartWebServer can detect this via HasPrefix and roll back the
		// Apache/Nginx start — without that, the frontend would still
		// proceed to start the web server and the user would see a vague
		// "Service Unavailable" 503 from Apache (which can't reach the
		// never-started PHP FastCGI worker on 8001) instead of the real
		// cause: the PHP SAPI binary is missing from the released zip.
		//
		// We detect two scenarios:
		//   1) Version dir exists but only ships `php` (CLI), no SAPI.
		//      This is a packaging issue in the released PHP zip.
		//   2) Version dir doesn't exist at all — version not installed.
		verDir := filepath.Join(base, version)
		cliOnly := false
		if info, statErr := os.Stat(verDir); statErr == nil && info.IsDir() {
			// Version folder exists. Does it have any `php` binary (CLI)?
			for _, candidate := range []string{
				filepath.Join(verDir, "php"),
				filepath.Join(verDir, "bin", "php"),
			} {
				if pi, e := os.Stat(candidate); e == nil && !pi.IsDir() {
					cliOnly = true
					break
				}
			}
		}
		if cliOnly {
			return "", nil, fmt.Errorf(
				"Error: PHP %s hanya berisi binary CLI (php), tanpa %s. "+
					"Bundel zip untuk PHP %s tidak menyertakan SAPI FastCGI — "+
					"re-pack zip-nya agar menyertakan php-fpm (untuk versi 8.x) "+
					"atau php-cgi (untuk 7.4). Lihat: ~/.zampp/bin/php/%s/",
				version, sapiName, version, version,
			)
		}
		return "", nil, fmt.Errorf(
			"Error: %s untuk PHP %s belum terpasang di ~/.zampp/bin/php/%s/ "+
				"(diperiksa: ./%s dan ./bin/%s). Pastikan versi PHP tersebut sudah di-download.",
			sapiName, version, version, sapiName, sapiName,
		)
	}

	if isCGI {
		// php-cgi supports the FastCGI bind flag directly.
		return bp, []string{"-b", "127.0.0.1:" + phpInternalPort}, nil
	}

	// php-fpm: generate/update the pool config, then pass --fpm-config.
	cfgPath, err := writePHPFPMConfig(version)
	if err != nil {
		return "", nil, fmt.Errorf("cannot write php-fpm config: %w", err)
	}
	// -F keeps FPM in the foreground so the spawned process keeps running.
	// Without -F, php-fpm daemonizes and the master exits immediately,
	// leaving cmd.Start() with a defunct process.
	return bp, []string{"-F", "--fpm-config", cfgPath}, nil
}

// writePHPFPMConfig renders the minimal FPM pool config for a PHP version
// into ~/.zampp/config/php-fpm-{version}.conf and returns its absolute path.
//
// listen = 127.0.0.1:8001 — the internal FastCGI port Nginx/Apache proxies to.
// pm = dynamic with conservative child counts suitable for local dev.
//
// The directory ~/.zampp/config is created on demand (ensureAppDirs also
// creates it during startup; this is a per-call safety net).
func writePHPFPMConfig(version string) (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	cfgDir := filepath.Join(root, "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return "", fmt.Errorf("cannot create config dir %s: %w", cfgDir, err)
	}

	cfgPath := filepath.Join(cfgDir, fmt.Sprintf("php-fpm-%s.conf", version))
	content := fmt.Sprintf(`[global]
pid = /tmp/zampp-php-fpm.pid
error_log = /tmp/zampp-php-fpm.log

[www]
listen = 127.0.0.1:%s
pm = dynamic
pm.max_children = 5
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 3
`, phpInternalPort)

	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("cannot write php-fpm config %s: %w", cfgPath, err)
	}
	return cfgPath, nil
}

// probeBinaryRuns executes the PHP SAPI binary briefly (with the same args
// StartPHP would use) to catch immediate-failure errors that don't reach the
// listening stage — most notably "dyld: Library not loaded" on macOS when the
// bundled PHP was packaged without required shared libs. We run the binary
// with a 1200ms timeout. A nil error means it ran long enough that we
// couldn't gather diagnostic info either way; non-nil means we captured a
// startup error.
//
// We use a separate exec.Cmd (not the one StartPHP already started) so the
// original process state is not perturbed.
func probeBinaryRuns(binaryPath string, args []string) error {
	// For php-cgi, ask for -v (version) — that exercises dyld and core
	// library loading without binding any port. For php-fpm, -v also works.
	probe := exec.Command(binaryPath, "-v")
	out, err := probe.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			return fmt.Errorf("%s (no output)", err.Error())
		}
		return fmt.Errorf("%s: %s", err.Error(), text)
	}
	return nil
}

// StartPHP launches the PHP FastCGI worker on 127.0.0.1:8001 for the given
// version. The worker is spawned in the background (non-blocking) using
// cmd.Start(); Nginx/Apache on :8000 then proxies .php requests here.
//
// Version-specific SAPI:
//   - 7.4   → php-cgi -b 127.0.0.1:8001
//   - 8.x+  → php-fpm -F --fpm-config ~/.zampp/config/php-fpm-{version}.conf
//             (config carries `listen = 127.0.0.1:8001`)
func (a *App) StartPHP(version string) string {
	if version == "" {
		return "Error: PHP version is empty"
	}

	binaryPath, args, err := phpLaunchCommand(version)
	if err != nil {
		return err.Error()
	}

	// Ensure it is executable. If not, try to fix it.
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Sprintf("Error: cannot make %s executable: %s", binaryPath, err.Error())
	}

	docRoot, err := htdocsPath()
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error())
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Sprintf("Error: cannot create htdocs at %s: %s", docRoot, err.Error())
	}

	// Stop any existing PHP server on the internal port before starting a new one.
	a.StopPHP()
	// Wait briefly for the port to actually free up after StopPHP — php-cgi
	// and php-fpm can take a few hundred ms to release the listening socket
	// after SIGTERM. If we Start() immediately, the new process can fail to
	// bind with "Address already in use" and the port never comes up.
	for i := 0; i < 30; i++ {
		if !portListenerExists(phpInternalPort) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cmd := exec.Command(binaryPath, args...)

	// Stream stdout/stderr to log file + parent terminal so PHP startup
	// errors are not lost. We tee BOTH stdout and stderr into the safeBuffer —
	// some PHP SAPI builds (notably certain php-cgi builds) write their fatal
	// startup errors to stdout instead of stderr, so capturing only stderr
	// leaves the user with an empty diagnostic and a misleading "Periksa log".
	phpLogDir, _ := phpLogDir()
	startupBuffer := newSafeBuffer()
	attachLogger(cmd, phpLogDir, fmt.Sprintf("php-%s.log", version), startupBuffer)
	// Also tee stdout into the startup buffer so we catch php-cgi failures
	// that print to stdout. attachLogger normally wires cmd.Stdout to a
	// MultiWriter(file, os.Stdout); we wrap it again here to also feed the
	// startupBuffer. (cmd.Stdout may already be an io.MultiWriter; we just
	// compose one more level.)
	if cmd.Stdout != nil {
		cmd.Stdout = io.MultiWriter(cmd.Stdout, startupBuffer)
	} else {
		cmd.Stdout = startupBuffer
	}

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start PHP %s: %s", version, err.Error())
	}

	phpProcess = cmd
	fmt.Printf("[php] started PID %d: %s %v\n", cmd.Process.Pid, binaryPath, args)
	// Do NOT call Process.Release() — php-cgi (-b) and php-fpm (-F) both run
	// in foreground mode and stay alive as our child. We want a live handle
	// for StopPHP (Signal/Kill) + cmd.Wait() (reap zombie).

	// Verify the PHP worker actually bound the FastCGI port. php-fpm can fail
	// on startup (bad config, missing module, port in use) even though
	// cmd.Start() returned nil.
	if !waitForPort(phpInternalPort, 3000*time.Millisecond) {
		startupText := strings.TrimSpace(startupBuffer.String())
		// Inspect process exit state: did the child die immediately, or
		// is it still alive but not listening? This distinguishes a crash
		// from a silent bind failure.
		pid := cmd.Process.Pid
		aliveErr := cmd.Process.Signal(os.Signal(nil))
		fmt.Printf("[php] waitForPort failed. PID=%d aliveErr=%v startupText=%q\n",
			pid, aliveErr, startupText)
		a.StopPHP()
		// Also probe whether the process is actually still alive — if
		// php-cgi forked & exited immediately, the port won't come up and
		// the child may already be a zombie.
		if startupText != "" {
			return fmt.Sprintf("Error: PHP %s gagal start (port %s tidak listening): %s", version, phpInternalPort, startupText)
		}
		// Process exited silently with no output — give the user something
		// actionable instead of just "Periksa log" (which is empty).
		// First verify the binary itself runs at all (e.g. dyld errors).
		if err := probeBinaryRuns(binaryPath, args); err != nil {
			return fmt.Sprintf("Error: PHP %s gagal start (port %s tidak listening): binary probe failed: %s", version, phpInternalPort, err.Error())
		}
		return fmt.Sprintf("Error: PHP %s gagal start (port %s tidak listening dalam 3s, tidak ada output). Periksa ~/.zampp/logs/php/php-%s.log", version, phpInternalPort, version)
	}

	sapi := "php-fpm"
	if version == "7.4" {
		sapi = "php-cgi"
	}
	return fmt.Sprintf("Started PHP %s (%s) on 127.0.0.1:%s (docroot: %s)", version, sapi, phpInternalPort, docRoot)
}

// StopPHP stops the PHP process currently listening on the internal port (8001).
func (a *App) StopPHP() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Error: StopPHP panic: %v", r)
		}
	}()

	// Primary path: kill the tracked PHP process if we still hold a reference
	// to it. Never target a process group (no -PID, no PID 0 or negative).
	if phpProcess != nil && phpProcess.Process != nil {
		pid := phpProcess.Process.Pid
		if pid > 0 {
			// SIGTERM first so php-fpm/php-cgi can clean up.
			if err := phpProcess.Process.Signal(syscall.SIGTERM); err != nil {
				_ = phpProcess.Process.Kill()
			}
			// Reap zombie in background.
			go func(c *exec.Cmd) {
				if c != nil {
					_ = c.Wait()
				}
			}(phpProcess)
			phpProcess = nil
			return fmt.Sprintf("stopped PHP (PID %d)", pid)
		}
		phpProcess = nil
	}

	// Secondary fallback: find the listener on the PHP internal port via lsof
	// and kill that exact PID only.
	out, err := exec.Command("lsof", "-ti", "tcp:"+phpInternalPort, "-sTCP:LISTEN").Output()
	if err != nil {
		return "Info: no process found on PHP port " + phpInternalPort + " (already stopped)"
	}

	pids := strings.TrimSpace(string(out))
	if pids == "" {
		return "Info: no process found on PHP port " + phpInternalPort + " (already stopped)"
	}

	var messages []string
	for _, pid := range strings.Split(pids, "\n") {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		// Safety: never kill PID 0 or non-numeric/negative values.
		if pid == "0" || strings.HasPrefix(pid, "-") {
			continue
		}
		if err := exec.Command("kill", "-TERM", pid).Run(); err != nil {
			messages = append(messages, fmt.Sprintf("failed to kill PID %s: %s", pid, err.Error()))
		} else {
			messages = append(messages, fmt.Sprintf("stopped PID %s", pid))
		}
	}

	if len(messages) == 0 {
		return "Info: no process stopped"
	}
	return strings.Join(messages, "; ")
}

// GetInstalledVersions returns the sorted list of PHP versions that have a
// binary installed under ~/.zampp/bin/php/<version>/ (supporting both
// the SPC layout ./php and the MAMP layout ./bin/php).
func (a *App) GetInstalledVersions() []string {
	base, err := phpBaseDir()
	if err != nil {
		return []string{}
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		return []string{}
	}

	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, err := getPHPExecutablePath(name); err == nil {
			versions = append(versions, name)
		}
	}
	sort.Strings(versions)
	return versions
}

// IsMySQLInstalled reports whether the mysqld binary is present, so the
// frontend can show an Installed / Not Installed status.
func (a *App) IsMySQLInstalled() bool {
	return isMySQLInstalled()
}

// nginxBaseDir returns the absolute path under which the Nginx binary is stored
// (e.g. ~/.zampp/bin/nginx).
func nginxBaseDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", "nginx"), nil
}

// nginxBinaryPath returns the absolute path to the nginx binary
// (e.g. ~/.zampp/bin/nginx/nginx).
func nginxBinaryPath() (string, error) {
	base, err := nginxBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "nginx"), nil
}

// nginxConfDir returns the absolute path to the Nginx config directory
// (e.g. ~/.zampp/conf/nginx).
func nginxConfDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "conf", "nginx"), nil
}

// nginxConfPath returns the absolute path to nginx.conf.
func nginxConfPath() (string, error) {
	d, err := nginxConfDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "nginx.conf"), nil
}

// nginxLogDir returns the absolute path to the Nginx logs directory
// (e.g. ~/.zampp/logs/nginx).
func nginxLogDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "logs", "nginx"), nil
}

// phpLogDir returns the absolute path to the PHP per-version logs directory
// (e.g. ~/.zampp/logs/php). Each PHP version gets its own log file inside.
func phpLogDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "logs", "php"), nil
}

// isNginxInstalled reports whether the nginx binary exists.
func isNginxInstalled() bool {
	p, err := nginxBinaryPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// IsNginxInstalled exposes nginx-installation status to the frontend.
func (a *App) IsNginxInstalled() bool {
	return isNginxInstalled()
}

// nginxTempDirs returns the list of temp directories nginx needs writable
// access to at runtime: client_body, proxy, fastcgi, uwsgi, scgi temp paths
// under ~/.zampp/tmp/nginx/.
//
// Without these, nginx falls back to its compile-time defaults — for the
// ZAMPP binary those are baked-in as /Applications/MAMP/... paths which do
// not exist on end-user machines, producing errors like
//   mkdir() "/Applications/MAMP/Library/client_body_temp" failed (2: No such file or directory)
// We override via `client_body_temp_path` etc in nginx.conf AND pre-create
// them here so the first request doesn't 500 trying to mkdir on the fly.
func nginxTempDirs() ([]string, error) {
	root, err := appRootDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(root, "tmp", "nginx")
	subs := []string{
		"client_body",
		"proxy",
		"fastcgi",
		"uwsgi",
		"scgi",
	}
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, filepath.Join(base, s))
	}
	return out, nil
}

// ensureNginxTempDirs creates the nginx temp directories and the nginx log
// directory before nginx is started. All paths are absolute under ~/.zampp/
// so they are writable regardless of where the nginx binary was compiled to
// expect logs and temp files.
func ensureNginxTempDirs() error {
	logDir, err := nginxLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("cannot create nginx log dir %s: %w", logDir, err)
	}
	dirs, err := nginxTempDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("cannot create nginx temp dir %s: %w", d, err)
		}
	}
	return nil
}

// GenerateNginxConfig writes a fresh nginx.conf into ~/.zampp/conf/nginx/.
// It is called on startup and on every StartNginx, so config always reflects
// the current port/docroot settings. The conf dir is created if missing.
func GenerateNginxConfig() error {
	confDir, err := nginxConfDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("cannot create nginx conf dir: %w", err)
	}

	docRoot, err := htdocsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Errorf("cannot create htdocs: %w", err)
	}

	logDir, err := nginxLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("cannot create nginx log dir: %w", err)
	}
	// Pre-create the temp directories (client_body, proxy, fastcgi, uwsgi,
	// scgi) so nginx never falls back to its compile-time MAMP defaults.
	if err := ensureNginxTempDirs(); err != nil {
		return fmt.Errorf("cannot prepare nginx temp dirs: %w", err)
	}

	accessLog := filepath.Join(logDir, "access.log")
	errorLog := filepath.Join(logDir, "error.log")
	pidFile := filepath.Join(logDir, "nginx.pid")

	// Compute absolute temp paths under ~/.zampp/tmp/nginx/ so they fully
	// override the MAMP-baked defaults baked into the binary
	// (/Applications/MAMP/Library/client_body_temp etc.).
	tmpDirs, err := nginxTempDirs()
	if err != nil {
		return err
	}
	clientBodyTmp := tmpDirs[0]
	proxyTmp := tmpDirs[1]
	fastcgiTmp := tmpDirs[2]
	uwsgiTmp := tmpDirs[3]
	scgiTmp := tmpDirs[4]

	// nginx.conf generator.
	//
	// Critical: PHP backend on port 8001 is a FastCGI server (php-cgi -b or
	// php-fpm) — NOT an HTTP server. The previous config used
	//     proxy_pass http://127.0.0.1:8001;
	// which sends an HTTP request to the FastCGI port. PHP immediately closes
	// the connection (it only speaks the FastCGI binary protocol), nginx logs
	//
	//     kevent() ... Connection reset by peer while reading response header
	//     from upstream, upstream: "http://127.0.0.1:8001/index.php"
	//
	// and returns 502 Bad Gateway. The correct directive is `fastcgi_pass`,
	// which speaks the FastCGI protocol.
	//
	// All paths (pid, error_log, access_log, *_temp_path) are absolute under
	// ~/.zampp/ to override the compile-time MAMP defaults that point at
	// /Applications/MAMP/... which doesn't exist on end-user machines.
	conf := "worker_processes  1;\n" +
		"pid " + pidFile + ";\n" +
		"error_log " + errorLog + ";\n\n" +
		"events {\n" +
		"    worker_connections  1024;\n" +
		"}\n\n" +
		"http {\n" +
		// Override compile-time temp paths so nginx never tries to mkdir
		// under /Applications/MAMP/Library/... (which fails with ENOENT).
		"    client_body_temp_path " + clientBodyTmp + ";\n" +
		"    proxy_temp_path       " + proxyTmp + ";\n" +
		"    fastcgi_temp_path     " + fastcgiTmp + ";\n" +
		"    uwsgi_temp_path       " + uwsgiTmp + ";\n" +
		"    scgi_temp_path        " + scgiTmp + ";\n\n" +
		"    access_log  " + accessLog + ";\n" +
		"    error_log   " + errorLog + ";\n\n" +
		"    server {\n" +
		"        listen       " + nginxPort + ";\n" +
		"        server_name  localhost;\n\n" +
		"        root   " + docRoot + ";\n" +
		"        index  index.php index.html index.htm;\n\n" +
		"        location / {\n" +
		"            try_files $uri $uri/ =404;\n" +
		"        }\n\n" +
		// Hand off .php to the FastCGI worker on 8001. SCRIPT_FILENAME is
		// constructed from $document_root + $fastcgi_script_name and is the
		// absolute path PHP needs (resolves 'No input file specified.').
		"        location ~ \\.php$ {\n" +
		"            fastcgi_pass   127.0.0.1:" + phpInternalPort + ";\n" +
		"            fastcgi_index  index.php;\n" +
		"            fastcgi_param  SCRIPT_FILENAME  $document_root$fastcgi_script_name;\n" +
		"            include        fastcgi_params;\n" +
		"        }\n" +
		"    }\n" +
		"}\n"

	confPath, err := nginxConfPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(conf), 0644); err != nil {
		return fmt.Errorf("cannot write nginx.conf: %w", err)
	}

	// Write a minimal fastcgi_params file next to nginx.conf. The official
	// nginx build ships this under conf/fastcgi_params, but the ZAMPP nginx
	// binary distribution only contains the `nginx` executable (no conf/
	// directory). Without this file, `include fastcgi_params;` would
	// fail with `open() .../fastcgi_params failed (2: No such file or
	// directory)` and nginx -t would report config test is failed.
	fcgiParamsPath := filepath.Join(filepath.Dir(confPath), "fastcgi_params")
	if err := os.WriteFile(fcgiParamsPath, []byte(fastcgiParamsContent), 0644); err != nil {
		return fmt.Errorf("cannot write fastcgi_params: %w", err)
	}

	return nil
}

// fastcgiParamsContent is the standard set of FastCGI variables nginx passes
// to PHP. Mirrors the upstream conf/fastcgi_params file shipped with nginx.
// SCRIPT_FILENAME is intentionally NOT included here — it is set explicitly
// in the location ~ \.php$ block in the generated nginx.conf so we have full
// control over its form ($document_root + $fastcgi_script_name, the canonical
// mapping for php-cgi/php-fpm).
const fastcgiParamsContent = `fastcgi_param  QUERY_STRING       $query_string;
fastcgi_param  REQUEST_METHOD     $request_method;
fastcgi_param  CONTENT_TYPE       $content_type;
fastcgi_param  CONTENT_LENGTH     $content_length;

fastcgi_param  SCRIPT_NAME        $fastcgi_script_name;
fastcgi_param  REQUEST_URI        $request_uri;
fastcgi_param  DOCUMENT_URI       $document_uri;
fastcgi_param  DOCUMENT_ROOT      $document_root;
fastcgi_param  SERVER_PROTOCOL    $server_protocol;
fastcgi_param  REQUEST_SCHEME     $scheme;
fastcgi_param  HTTPS              $https if_not_empty;

fastcgi_param  GATEWAY_INTERFACE  CGI/1.1;
fastcgi_param  SERVER_SOFTWARE    nginx/$nginx_version;

fastcgi_param  REMOTE_ADDR        $remote_addr;
fastcgi_param  REMOTE_PORT        $remote_port;
fastcgi_param  SERVER_ADDR        $server_addr;
fastcgi_param  SERVER_PORT        $server_port;
fastcgi_param  SERVER_NAME        $server_name;

fastcgi_param  REDIRECT_STATUS    200;
`

// StartNginx starts the standalone nginx server using the generated conf.
func (a *App) StartNginx() string {
	if nginxProcess != nil && nginxProcess.Process != nil {
		// Check if the process is still alive.
		if err := nginxProcess.Process.Signal(os.Signal(nil)); err == nil {
			return "Info: Nginx is already running"
		}
	}

	binaryPath, err := nginxBinaryPath()
	if err != nil {
		return err.Error()
	}

	// Check that the binary file is present.
	if _, err := os.Stat(binaryPath); err != nil {
		return "Nginx Not Installed — letakkan binary di ~/.zampp/bin/nginx/nginx"
	}

	// Ensure it is executable.
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Sprintf("Error: cannot make %s executable: %s", binaryPath, err.Error())
	}

	// (Re)generate nginx.conf so it always reflects current settings.
	if err := GenerateNginxConfig(); err != nil {
		return fmt.Sprintf("Error: gagal membuat nginx.conf: %s", err.Error())
	}

	confPath, err := nginxConfPath()
	if err != nil {
		return err.Error()
	}

	// Defensive cleanup of any stale nginx on our port.
	a.stopNginxOnPort()

	// Use `-p <prefix>` to override the compile-time prefix baked into the
	// nginx binary (for ZAMPP that's /Applications/MAMP/Library/... which
	// doesn't exist on end-user machines and causes errors like
	//   open() "/Applications/MAMP/logs/nginx_error.log" failed
	//   mkdir() "/Applications/MAMP/Library/client_body_temp" failed
	// `-p ~/.zampp/` makes nginx resolve every relative path (include
	// fastcgi_params, etc.) against the ZAMPP prefix instead. Combined with
	// the absolute paths in *_temp_path/error_log/pid directives above, this
	// supports the MAMP-baked defaults so the binary is portable.
	appRoot, err := appRootDir()
	if err != nil {
		return err.Error()
	}
	// Pass error_log via `-g` so it takes effect as early as possible. nginx
	// 1.19.2 still emits a single pre-config "alert" line to its compile-
	// time default error_log path (/Applications/MAMP/...) before reading
	// the `-g` directives, but crucially the /Applications/MAMP directory
	// is NOT created when the `-g` error_log is in place — nginx fails to
	// open the default path, logs one alert, then switches to our `-g`
	// path. Without `-g`, /Applications/MAMP/ gets created by nginx trying
	// to mkdir-and-open the default path on every start.
	//
	// `pid` is intentionally NOT in `-g` — it would duplicate the pid
	// directive already in nginx.conf, which nginx treats as an emerg-level
	// "duplicate directive" error.
	logDir, _ := nginxLogDir()
	globalDirectives := fmt.Sprintf(
		"error_log %s; daemon off;",
		filepath.Join(logDir, "error.log"),
	)
	cmd := exec.Command(binaryPath,
		"-p", appRoot+"/",
		"-c", confPath,
		"-g", globalDirectives,
	)

	// Stream stdout/stderr to log file + parent terminal so nginx startup
	// errors (missing module, bad config) are visible in `wails dev` and in
	// ~/.zampp/logs/nginx/stdout.log. extraStderr captures them for the
	// synchronous error message below.
	usedLogDir, _ := nginxLogDir()
	stderrBuffer := newSafeBuffer()
	attachLogger(cmd, usedLogDir, "stdout.log", stderrBuffer)

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start nginx: %s", err.Error())
	}

	nginxProcess = cmd
	// Do NOT call Process.Release() — with `daemon off;` (set in -g) the
	// nginx master stays alive as our child and we want a live handle for
	// StopNginx (Signal/Kill) + cmd.Wait() (reap zombie).

	// Verify nginx really bound the port; if it crashed on startup (bad
	// config, missing module, port in use) report it instead of pretending
	// the server is up.
	if !waitForPort(nginxPort, 3000*time.Millisecond) {
		stderrText := strings.TrimSpace(stderrBuffer.String())
		a.StopNginx()
		if stderrText != "" {
			return fmt.Sprintf("Error: Nginx gagal start (port %s tidak listening): %s", nginxPort, stderrText)
		}
		return fmt.Sprintf("Error: Nginx gagal start (port %s tidak listening dalam 3s). Periksa ~/.zampp/logs/nginx/stdout.log", nginxPort)
	}

	return fmt.Sprintf("Started Nginx on port %s (conf: %s)", nginxPort, confPath)
}

// StopNginx stops the running nginx process, if any.
//
// Safety contract:
//   - Never targets a process group (no `kill -- -PID`, no PID 0/negative).
//   - Primary path: kill the tracked *exec.Cmd's process only.
//   - Fallback A: `nginx -s stop -c <conf>` (nginx's own graceful stop).
//   - Fallback B: `pkill -f nginx` (matches the nginx command line, no PID).
//   - Fallback C: per-PID `kill -TERM <pid>` for listeners on nginxPort.
func (a *App) StopNginx() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Error: StopNginx panic: %v", r)
		}
	}()

	var messages []string

	// Primary path: kill the tracked process only.
	if nginxProcess != nil && nginxProcess.Process != nil {
		pid := nginxProcess.Process.Pid
		if pid > 0 {
			// SIGTERM for graceful shutdown (nginx handles it cleanly).
			if err := nginxProcess.Process.Signal(syscall.SIGTERM); err != nil {
				_ = nginxProcess.Process.Kill()
			}
			// Reap zombie in background.
			go func(c *exec.Cmd) {
				if c != nil {
					_ = c.Wait()
				}
			}(nginxProcess)
			messages = append(messages, fmt.Sprintf("stopped Nginx (PID %d)", pid))
		}
		nginxProcess = nil
	}

	// Fallback A: nginx's own graceful stop, using the generated conf.
	if confPath, err := nginxConfPath(); err == nil {
		if binaryPath, err := nginxBinaryPath(); err == nil {
			_ = exec.Command(binaryPath, "-s", "stop", "-c", confPath).Run()
		}
	}

	// Fallback B: kill the exact PID listening on nginxPort (defensive).
	// We do NOT run `pkill -f nginx` — that would kill every nginx on the
	// system (Homebrew, MAMP, etc).
	msgs := a.stopNginxOnPort()
	if msgs != "" && !strings.HasPrefix(msgs, "Info:") {
		messages = append(messages, msgs)
	}

	if len(messages) == 0 {
		return "Info: Nginx already stopped"
	}
	return strings.Join(messages, "; ")
}

// stopNginxOnPort kills any process listening on the nginx port. Used as a
// last-resort cleanup. It never uses process-group kills.
func (a *App) stopNginxOnPort() string {
	out, err := exec.Command("lsof", "-ti", "tcp:"+nginxPort, "-sTCP:LISTEN").Output()
	if err != nil {
		return "Info: no Nginx process found on port " + nginxPort + " (already stopped)"
	}
	pids := strings.TrimSpace(string(out))
	if pids == "" {
		return "Info: no Nginx process found on port " + nginxPort + " (already stopped)"
	}
	var messages []string
	for _, pid := range strings.Split(pids, "\n") {
		pid = strings.TrimSpace(pid)
		// Safety: never kill PID 0 or negative values.
		if pid == "" || pid == "0" || strings.HasPrefix(pid, "-") {
			continue
		}
		if err := exec.Command("kill", "-TERM", pid).Run(); err != nil {
			messages = append(messages, fmt.Sprintf("failed to kill PID %s: %s", pid, err.Error()))
		} else {
			messages = append(messages, fmt.Sprintf("stopped PID %s", pid))
		}
	}
	if len(messages) == 0 {
		return "Info: no Nginx process stopped"
	}
	return strings.Join(messages, "; ")
}

// apacheBaseDir returns the absolute path under which the Apache httpd binary
// and modules are stored (e.g. ~/.zampp/bin/apache).
func apacheBaseDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", "apache"), nil
}

// apacheBinaryPath returns the absolute path to the httpd binary
// (e.g. ~/.zampp/bin/apache/httpd).
func apacheBinaryPath() (string, error) {
	base, err := apacheBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "httpd"), nil
}

// apacheConfDir returns the absolute path to the Apache config directory
// (e.g. ~/.zampp/conf/apache).
func apacheConfDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "conf", "apache"), nil
}

// apacheConfPath returns the absolute path to httpd.conf.
func apacheConfPath() (string, error) {
	d, err := apacheConfDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "httpd.conf"), nil
}

// apacheLogDir returns the absolute path to the Apache logs directory
// (e.g. ~/.zampp/logs/apache).
func apacheLogDir() (string, error) {
	root, err := appRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "logs", "apache"), nil
}

// isApacheInstalled reports whether the httpd binary exists.
func isApacheInstalled() bool {
	p, err := apacheBinaryPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// IsApacheInstalled exposes apache-installation status to the frontend.
func (a *App) IsApacheInstalled() bool {
	return isApacheInstalled()
}

// portListenerExists returns true if any process is listening on the
// given TCP port. Used to verify that a daemonized web server (nginx or
// httpd) is actually running, since the *exec.Cmd tracker points to the
// parent process which exits immediately after forking the worker.
func portListenerExists(port string) bool {
	out, err := exec.Command("lsof", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// waitForPort polls lsof for a TCP listener on the given port up to the
// specified timeout. Returns true once the port is listening, false if the
// deadline elapses first. The poll interval is 100ms.
//
// This is used by StartApache/StartNginx to detect startup failures where
// exec() succeeds (cmd.Start() returns nil) but the process crashes within
// milliseconds (e.g. dynamic loader errors, missing modules, port in use).
// Without this check, the frontend would believe the server is up.
func waitForPort(port string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portListenerExists(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// adminerDownloadURL is the official Adminer single-file PHP release.
const adminerDownloadURL = "https://github.com/vrana/adminer/releases/download/v4.8.1/adminer-4.8.1-en.php"

// ensureAdminerDownloaded makes sure ~/.zampp/htdocs/adminer.php exists.
// If it does not, it downloads the official Adminer PHP file from GitHub
// Releases and writes it directly into the htdocs directory. This is an
// on-demand download — it only runs the first time the user clicks the
// Adminer button.
func ensureAdminerDownloaded() error {
	docRoot, err := htdocsPath()
	if err != nil {
		return fmt.Errorf("cannot resolve htdocs: %w", err)
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Errorf("cannot create htdocs at %s: %w", docRoot, err)
	}

	target := filepath.Join(docRoot, "adminer.php")

	// Already present — nothing to do.
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		return nil
	}

	// Download Adminer PHP file directly to the target path.
	req, err := http.NewRequest(http.MethodGet, adminerDownloadURL, nil)
	if err != nil {
		return fmt.Errorf("cannot create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download Adminer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("adminer download failed: HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", target, err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(target)
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	out.Close()
	return nil
}

// OpenAdminer opens Adminer in the user's default browser via the macOS
// `open` command. It routes the URL through whichever web server is currently
// running (preferring Nginx :8000, falling back to Apache :8000), and prefills
// the MySQL server field to 127.0.0.1:<mySQLPort> so login works against the
// app's standalone MySQL on port 3307 (not XAMPP's 3306).
//
// On-Demand: if ~/.zampp/htdocs/adminer.php does not exist yet, it is
// downloaded from the official Adminer GitHub release before opening.
func (a *App) OpenAdminer() string {
	// 1) Ensure Adminer is present in htdocs (download on-demand).
	if err := ensureAdminerDownloaded(); err != nil {
		return fmt.Sprintf("Error: %s", err.Error())
	}

	// 2) Resolve the running web server's base URL.
	var baseURL string
	if nginxProcess != nil && nginxProcess.Process != nil {
		if err := nginxProcess.Process.Signal(os.Signal(nil)); err == nil {
			baseURL = "http://127.0.0.1:" + nginxPort
		}
	}
	// Nginx/httpd daemonize: the tracked parent exits after forking the
	// worker, so the process tracker can be stale. Fall back to checking
	// whether anything is actually listening on the ports.
	if baseURL == "" && portListenerExists(nginxPort) {
		baseURL = "http://127.0.0.1:" + nginxPort
	}
	if baseURL == "" && apacheProcess != nil && apacheProcess.Process != nil {
		if err := apacheProcess.Process.Signal(os.Signal(nil)); err == nil {
			baseURL = "http://127.0.0.1:" + apachePort
		}
	}
	if baseURL == "" && portListenerExists(apachePort) {
		baseURL = "http://127.0.0.1:" + apachePort
	}
	if baseURL == "" {
		return "Error: jalankan Nginx atau Apache terlebih dahulu"
	}

	// 3) Build Adminer URL with prefilled MySQL server (127.0.0.1:3307) and
	//    username=root so the user only needs the password.
	url := baseURL + "/adminer.php?server=127.0.0.1:" + mySQLPort + "&username=root&password=root"

	if err := exec.Command("open", url).Start(); err != nil {
		return fmt.Sprintf("Error: gagal membuka browser: %s", err.Error())
	}
	return "Opened Adminer at " + url
}

// OpenWebRoot opens the running web server's root URL (http://127.0.0.1:8000)
// in the user's default browser via the macOS `open` command. The WebView's
// window.open does not reliably launch an external browser on macOS, so we
// shell out to `open` instead.
func (a *App) OpenWebRoot() string {
	if !portListenerExists(nginxPort) {
		return "Error: jalankan Web Server terlebih dahulu"
	}
	url := "http://127.0.0.1:" + nginxPort
	if err := exec.Command("open", url).Start(); err != nil {
		return fmt.Sprintf("Error: gagal membuka browser: %s", err.Error())
	}
	return "Opened WebRoot at " + url
}

// OpenHtdocsFolder opens the ~/.zampp/htdocs directory in Finder
// via the macOS `open` command. Useful because the folder is hidden under
// the user's home directory and inconvenient to navigate to manually.
func (a *App) OpenHtdocsFolder() error {
	docRoot, err := htdocsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Errorf("cannot create htdocs at %s: %w", docRoot, err)
	}
	if err := exec.Command("open", docRoot).Run(); err != nil {
		return fmt.Errorf("failed to open Finder: %w", err)
	}
	return nil
}

// composerPharURL is the official Composer stable phar download URL.
const composerPharURL = "https://getcomposer.org/composer-stable.phar"

// ensureComposerDownloaded makes sure ~/.zampp/bin/composer.phar exists. If it
// does not, it downloads the official Composer stable phar and writes it
// directly into ~/.zampp/bin/. This is an on-demand download — it only runs
// the first time the user opens the ZAMPP Terminal.
func ensureComposerDownloaded() error {
	root, err := appRootDir()
	if err != nil {
		return fmt.Errorf("cannot resolve app root: %w", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("cannot create bin dir %s: %w", binDir, err)
	}

	target := filepath.Join(binDir, "composer.phar")

	// Already present — nothing to do.
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		_ = os.Chmod(target, 0755)
		return nil
	}

	// Download Composer phar directly to the target path.
	req, err := http.NewRequest(http.MethodGet, composerPharURL, nil)
	if err != nil {
		return fmt.Errorf("cannot create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download Composer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("composer download failed: HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", target, err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(target)
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	out.Close()

	// Make it executable (chmod +x) so it can be invoked directly if needed.
	if err := os.Chmod(target, 0755); err != nil {
		return fmt.Errorf("cannot chmod %s: %w", target, err)
	}
	return nil
}

// OpenTerminal opens a macOS terminal (iTerm2 if installed, otherwise the
// built-in Terminal.app) pre-configured for ZAMPP. The activePhpVersion
// (e.g. "8.2") is injected into the shell PATH so the user gets that exact
// PHP binary, and a `composer` alias is created that runs the downloaded
// composer.phar with that PHP. The terminal starts in ~/.zampp/htdocs.
//
// On-Demand: if ~/.zampp/bin/composer.phar does not exist yet, it is
// downloaded from getcomposer.org before opening the terminal.
//
// The terminal session is scoped (no global PATH mutation) — only this shell
// session sees the ZAMPP php/composer overrides.
func (a *App) OpenTerminal(activePhpVersion string) error {
	version := strings.TrimSpace(activePhpVersion)
	if version == "" {
		return fmt.Errorf("activePhpVersion is empty")
	}

	// 1) Ensure composer.phar is present (download on-demand).
	if err := ensureComposerDownloaded(); err != nil {
		return err
	}

	// 2) Detect the active PHP version's folder layout:
	//    - static-php-cli (SPC): binary at ~/.zampp/bin/php/{version}/php
	//      → add the version dir itself to PATH.
	//    - MAMP layout:          binary at ~/.zampp/bin/php/{version}/bin/php
	//      → add the /bin subdir to PATH.
	//    This makes the exported PATH match whichever bundle was downloaded,
	//    so `php` in the terminal resolves to the correct binary.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve home dir: %w", err)
	}
	phpBase := filepath.Join(home, appDirName, "bin", "php", version)
	phpExecutablePath := phpBase // default: static-php-cli layout
	if _, err := os.Stat(filepath.Join(phpBase, "bin", "php")); err == nil {
		// MAMP layout: php binary lives under /bin
		phpExecutablePath = filepath.Join(phpBase, "bin")
	}

	// 3) Build the shell command string that will be executed in the
	//    terminal session. It exports the detected php folder to the front
	//    of PATH, aliases `composer` to use that PHP with the downloaded
	//    composer.phar, cd's into htdocs, prints a banner, and runs
	//    `php -v`.
	//
	//    Note: we escape the shell-side double-quotes as \", because the
	//    string will be embedded inside an AppleScript string literal that
	//    is itself wrapped in double-quotes — without escaping, the
	//    embedded " would prematurely close the AppleScript string and
	//    cause an osascript exit status 1.
	cmdString := fmt.Sprintf(`export PATH=\"%s:$PATH\"; alias composer='php $HOME/.zampp/bin/composer.phar'; cd $HOME/.zampp/htdocs; clear; echo '⚡️ ZAMPP Smart Terminal Ready!'; echo 'Active PHP: %s'; echo 'Composer is ready to use. Type ''composer'' to test.'; echo ''; php -v`, phpExecutablePath, version)

	// 4) Detect iTerm2 — if installed, prefer it over Terminal.app (most
	//    developers use iTerm2). Falls back to the built-in Terminal
	//    otherwise.
	var script string
	if _, err := os.Stat("/Applications/iTerm.app"); err == nil {
		// iTerm2: if no window is open, create a new window; otherwise
		// create a tab in the current window. This handles both the
		// "iTerm not running" and "iTerm already open" cases reliably
		// (the previous try/on error was fragile when iTerm was active).
		script = fmt.Sprintf(`
tell application "iTerm"
    activate
    if (count of windows) = 0 then
        set newWindow to (create window with default profile)
        tell current session of newWindow
            write text "%s"
        end tell
    else
        tell current window
            create tab with default profile
            tell current session
                write text "%s"
            end tell
        end tell
    end if
end tell
`, cmdString, cmdString)
	} else {
		// Fallback: built-in Terminal.app.
		script = fmt.Sprintf(`
tell application "Terminal"
    activate
    do script "%s"
end tell
`, cmdString)
	}

	// 5) Execute the AppleScript via osascript. The script is piped through
	//    stdin with "-" as the script argument, which is more reliable than
	//    passing a multi-line script via "-e <string>" — the latter can fail
	//    with exit status 1 when the embedded shell command contains quotes
	//    or newlines that confuse osascript's argv parser.
	cmd := exec.Command("osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to open terminal: %w", err)
	}
	return nil
}

// StartWebServer starts the PHP built-in server (internal port 8001) and then
// the chosen web server engine (nginx or apache) on port 8000. The UI calls
// this single API so it only needs one Start button + an engine dropdown.
//
// engine is "nginx" or "apache". phpVersion is the folder name under
// ~/.zampp/bin/php/ (e.g. "7.4", "8.2").
func (a *App) StartWebServer(engine string, phpVersion string) string {
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine != "nginx" && engine != "apache" {
		return "Error: engine harus \"nginx\" atau \"apache\""
	}

	// 1) Start PHP first on the internal port.
	phpResult := a.StartPHP(phpVersion)
	if strings.HasPrefix(phpResult, "Error") {
		return "Error: PHP gagal start — " + phpResult
	}

	// 2) Start the chosen engine.
	var engResult string
	switch engine {
	case "nginx":
		engResult = a.StartNginx()
	case "apache":
		engResult = a.StartApache()
	}
	if strings.HasPrefix(engResult, "Error") {
		// Roll back PHP if the web server failed to start.
		a.StopPHP()
		return engResult
	}

	return fmt.Sprintf("Started %s + PHP (port %s). %s | %s", engine, nginxPort, engResult, phpResult)
}

// StopWebServer stops the chosen web server engine (nginx or apache) first,
// then stops the PHP built-in server.
func (a *App) StopWebServer(engine string) string {
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine != "nginx" && engine != "apache" {
		return "Error: engine harus \"nginx\" atau \"apache\""
	}

	var engResult string
	switch engine {
	case "nginx":
		engResult = a.StopNginx()
	case "apache":
		engResult = a.StopApache()
	}

	phpResult := a.StopPHP()

	return engResult + " | " + phpResult
}

// defaultIndexPHP is the Welcome Screen shown when the user opens the web
// root for the first time. It reports the active PHP version and the running
// web server (Nginx or Apache) and links back to semut.dev.
const defaultIndexPHP = `<?php
$php_version = phpversion();
$raw_server = isset($_SERVER['SERVER_SOFTWARE']) ? strtolower($_SERVER['SERVER_SOFTWARE']) : 'unknown';

$server_name = 'Unknown';
$server_color = '#000000';

if (strpos($raw_server, 'nginx') !== false) {
    $server_name = 'Nginx';
    $server_color = '#009639';
} elseif (strpos($raw_server, 'apache') !== false) {
    $server_name = 'Apache';
    $server_color = '#D22128';
} else {
    $server_name = htmlspecialchars(explode(' ', $_SERVER['SERVER_SOFTWARE'])[0]);
}

$zampp_version = '1.0.0';
?>
<!DOCTYPE html>
<html>
<head>
    <title>Welcome to ZAMPP</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; background: #f4f4f5; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; }
        .container { background: white; padding: 40px; border-radius: 12px; box-shadow: 0 4px 6px rgba(0,0,0,0.05); text-align: center; max-width: 500px; width: 100%; }
        .logo { width: 120px; height: 120px; margin-bottom: 20px; border-radius: 22px; box-shadow: 0 4px 12px rgba(0,0,0,0.1); }
        h1 { color: #333; margin-bottom: 10px; font-size: 28px; font-weight: 700; }
        p { color: #666; margin: 15px 0; font-size: 16px; display: flex; justify-content: space-between; align-items: center; padding: 0 20px; }
        .info-box { background: #fafafa; border: 1px solid #eaeaea; border-radius: 8px; padding: 10px 0; margin-top: 25px; }
        .badge { padding: 6px 12px; border-radius: 6px; font-weight: bold; color: white; font-size: 14px; }
        .badge-php { background: #777BB4; }
        .footer { margin-top: 30px; font-size: 13px; color: #999; line-height: 1.6; }
        .footer a { color: #555; text-decoration: none; font-weight: 600; transition: color 0.2s; }
        .footer a:hover { color: #000; text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <img src="https://raw.githubusercontent.com/semutdev/zampp/main/img/zampp-logo.png" alt="ZAMPP Logo" class="logo">
        <h1>Welcome to ZAMPP</h1>
        <div style="color: #666; font-size: 15px; margin-bottom: 10px;">Your local development environment is running perfectly.</div>

        <div class="info-box">
            <p><strong>PHP Version</strong> <span class="badge badge-php"><?php echo $php_version; ?></span></p>
            <div style="height: 1px; background: #eaeaea; margin: 0 20px;"></div>
            <p><strong>Active Server</strong> <span class="badge" style="background: <?php echo $server_color; ?>;"><?php echo $server_name; ?></span></p>
        </div>

        <div class="footer">
            ZAMPP Version <?php echo $zampp_version; ?> &bull; macOS<br>
            Built with &#10084;&#65039; by <a href="https://semut.dev" target="_blank">semut.dev</a>
        </div>
    </div>
</body>
</html>`

// GenerateDefaultIndexPHP writes the Welcome page to ~/.zampp/htdocs/index.php
// only if it does not already exist. This guarantees new users see a default
// landing page the first time they open the web root, without overwriting an
// existing index.php the user may have placed there.
func GenerateDefaultIndexPHP() error {
	docRoot, err := htdocsPath()
	if err != nil {
		return fmt.Errorf("cannot resolve htdocs: %w", err)
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Errorf("cannot create htdocs at %s: %w", docRoot, err)
	}

	target := filepath.Join(docRoot, "index.php")

	// Do not clobber an existing index.php.
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		return nil
	}

	if err := os.WriteFile(target, []byte(defaultIndexPHP), 0644); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	return nil
}

// All paths are resolved to absolute form using the user's home directory.
// It is called on startup and on every StartApache so the config always
// reflects the current ports/paths.
func GenerateApacheConfig() error {
	home, err := homeDir()
	if err != nil {
		return err
	}
	appRoot := filepath.Join(home, appDirName)

	confDir, err := apacheConfDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("cannot create apache conf dir: %w", err)
	}

	docRoot, err := htdocsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(docRoot, 0755); err != nil {
		return fmt.Errorf("cannot create htdocs: %w", err)
	}

	logDir, err := apacheLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("cannot create apache log dir: %w", err)
	}
	pidFile := filepath.Join(logDir, "httpd.pid")

	serverRoot := filepath.Join(appRoot, "bin", "apache")

	var b strings.Builder
	// --- Global directives (must be OUTSIDE <VirtualHost>) ---
	b.WriteString("ServerRoot \"" + serverRoot + "\"\n")
	b.WriteString("Listen " + apachePort + "\n\n")
	b.WriteString("LoadModule unixd_module modules/mod_unixd.so\n")
	b.WriteString("LoadModule authz_core_module modules/mod_authz_core.so\n")
	b.WriteString("LoadModule dir_module modules/mod_dir.so\n")
	b.WriteString("LoadModule mime_module modules/mod_mime.so\n")
	b.WriteString("LoadModule rewrite_module modules/mod_rewrite.so\n")
	b.WriteString("LoadModule proxy_module modules/mod_proxy.so\n")
	b.WriteString("LoadModule proxy_fcgi_module modules/mod_proxy_fcgi.so\n\n")
	b.WriteString("ServerName localhost\n")
	b.WriteString("PidFile \"" + pidFile + "\"\n")
	b.WriteString("ErrorLog \"" + filepath.Join(logDir, "error.log") + "\"\n")
	b.WriteString("LogLevel warn\n")
	b.WriteString("ProxyTimeout 60\n\n")
	// AbsoluteHtdocsPath: Apache does not understand `~`, so we feed it the
	// fully-resolved absolute path returned by os.UserHomeDir() (already
	// absolute, e.g. /Users/jamal/.zampp/htdocs). The same value is reused
	// for DocumentRoot, the Directory block, and the fcgi:// URL prefix.
	absoluteHtdocsPath := docRoot

	// --- VirtualHost: contains all per-site rules per the requested template ---
	b.WriteString("<VirtualHost *:" + apachePort + ">\n")
	b.WriteString("    DocumentRoot \"" + absoluteHtdocsPath + "\"\n\n")
	// Route .php requests to the FastCGI worker (php-cgi/php-fpm) on 8001.
	//
	// Why SetHandler + ProxyFCGISetEnvIf and not ProxyPassMatch:
	//
	// 1) The classic pattern that works reliably for php-cgi (which is more
	//    picky than php-fpm about SCRIPT_FILENAME) on Apache 2.4.10+ is:
	//
	//       <FilesMatch \.php$>
	//           SetHandler "proxy:fcgi://127.0.0.1:8001"
	//       </FilesMatch>
	//       ProxyFCGISetEnvIf "true" SCRIPT_FILENAME "%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}"
	//
	//    mod_proxy_fcgi doesn't always derive SCRIPT_FILENAME the way
	//    php-cgi expects (it sets SCRIPT_NAME / PATH_INFO but leaves
	//    SCRIPT_FILENAME pointing at the URL rather than the mapped
	//    filesystem path). The explicit ProxyFCGISetEnvIf rewrites it to
	//    DOCUMENT_ROOT + SCRIPT_NAME — the canonical, valid value.
	//
	// 2) We previously tried ProxyPassMatch "fcgi://host:port{absRoot}/$1"
	//    and a trailing-slash-less SetHandler. Both still produced
	//    'No input file specified.' on Apache 2.4.54 + php-cgi.
	//
	// 3) The bare fcgi SetHandler sets the handler so Apache knows to proxy
	//    to FastCGI; ProxyFCGISetEnvIf adjusts the env passed via FastCGI.
	b.WriteString("    ProxyFCGISetEnvIf \"true\" SCRIPT_FILENAME \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\n")
	b.WriteString("    <FilesMatch \\.php$>\n")
	b.WriteString("        SetHandler \"proxy:fcgi://127.0.0.1:" + phpInternalPort + "\"\n")
	b.WriteString("    </FilesMatch>\n\n")
	b.WriteString("    <Directory \"" + absoluteHtdocsPath + "\">\n")
	b.WriteString("        Options Indexes FollowSymLinks\n")
	b.WriteString("        AllowOverride All\n")
	b.WriteString("        Require all granted\n")
	b.WriteString("    </Directory>\n\n")
	b.WriteString("    DirectoryIndex index.php index.html\n")
	b.WriteString("</VirtualHost>\n\n")
	b.WriteString("TypesConfig conf/mime.types\n")

	confPath, err := apacheConfPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("cannot write httpd.conf: %w", err)
	}
	return nil
}

// StartApache starts the standalone httpd server using the generated conf.
func (a *App) StartApache() string {
	if apacheProcess != nil && apacheProcess.Process != nil {
		// Check if the process is still alive.
		if err := apacheProcess.Process.Signal(os.Signal(nil)); err == nil {
			return "Info: Apache is already running"
		}
	}

	binaryPath, err := apacheBinaryPath()
	if err != nil {
		return err.Error()
	}

	// Check that the binary file is present.
	if _, err := os.Stat(binaryPath); err != nil {
		return "Apache Not Installed — letakkan binary di ~/.zampp/bin/apache/httpd"
	}

	// Ensure it is executable.
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Sprintf("Error: cannot make %s executable: %s", binaryPath, err.Error())
	}

	// (Re)generate httpd.conf so it always reflects current settings.
	if err := GenerateApacheConfig(); err != nil {
		return fmt.Sprintf("Error: gagal membuat httpd.conf: %s", err.Error())
	}

	confPath, err := apacheConfPath()
	if err != nil {
		return err.Error()
	}

	// Defensive cleanup of any stale httpd on our port.
	a.stopApacheOnPort()

	// -X is Apache's "debug mode" flag: single worker, does NOT detach from
	// the controlling terminal. This is what we actually want — without -X,
	// httpd daemonizes (parent forks workers and exits immediately), which
	// left us with no live process handle and caused the Stop button to
	// force-close the app via SIGCHLD/SIGHUP race. With -X, the httpd parent
	// stays as a direct child of this Go process and StopApache can reliably
	// signal + Wait for it.
	//
	// Note: Apache does NOT have a -DFOREGROUND define (common misconception).
	// The `-D name` flag only defines names for <IfDefine name> blocks in the
	// config; it does not change the daemonize behavior of MPM prefork.
	cmd := exec.Command(binaryPath, "-X", "-f", confPath)

	// Stream stdout/stderr to log file + parent terminal so httpd startup
	// errors (dyld "Library not loaded", missing modules, bind failures)
	// are visible in `wails dev` and in ~/.zampp/logs/apache/stdout.log.
	// extraStderr captures the first chunk of startup errors so we can
	// report them synchronously after a port-timeout below.
	apacheLogDir, _ := apacheLogDir()
	stderrBuffer := newSafeBuffer()
	attachLogger(cmd, apacheLogDir, "stdout.log", stderrBuffer)

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start apache: %s", err.Error())
	}

	apacheProcess = cmd
	// Do NOT call Process.Release() — with -DFOREGROUND the httpd parent
	// stays alive as our child and we want a live handle for StopApache
	// (Signal/Kill) + cmd.Wait() (reap zombie).

	// Wait briefly for the port to come up. httpd usually listens within a few
	// hundred ms; if the process crashed on startup (loader errors, port in
	// use, module load failure), the port check will stay false and we report
	// the stderr we captured instead of a misleading "Started Apache".
	if !waitForPort(apachePort, 3000*time.Millisecond) {
		stderrText := strings.TrimSpace(stderrBuffer.String())
		// Best-effort cleanup of any half-spawned process.
		a.StopApache()
		if stderrText != "" {
			return fmt.Sprintf("Error: Apache gagal start (port %s tidak listening): %s", apachePort, stderrText)
		}
		return fmt.Sprintf("Error: Apache gagal start (port %s tidak listening dalam 3s). Periksa ~/.zampp/logs/apache/stdout.log", apachePort)
	}

	return fmt.Sprintf("Started Apache on port %s (conf: %s)", apachePort, confPath)
}

// StopApache stops the running httpd process, if any.
//
// Safety contract:
//   - Never targets a process group (no `kill -- -PID`, no PID 0/negative).
//   - Never runs `pkill -f httpd` — that would kill ALL httpd on the system,
//     including other applications' Apache (XAMPP, Homebrew, MAMP).
//   - Primary path: kill the tracked *exec.Cmd's process only.
//   - Fallback A: `httpd -k stop -f <conf>` (Apache's own graceful stop).
//   - Fallback B: per-PID `kill -TERM <pid>` for listeners on apachePort.
//
// With -DFOREGROUND (used in StartApache), the httpd parent stays alive as a
// child of this Go process until killed here. We call cmd.Wait() in a
// goroutine after SIGTERM to reap the zombie.
func (a *App) StopApache() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Error: StopApache panic: %v", r)
		}
	}()

	var messages []string

	// Primary path: kill the tracked process only.
	if apacheProcess != nil && apacheProcess.Process != nil {
		pid := apacheProcess.Process.Pid
		if pid > 0 {
			// Try graceful SIGTERM first. With -DFOREGROUND, httpd parent
			// handles SIGTERM and propagates to workers.
			if err := apacheProcess.Process.Signal(syscall.SIGTERM); err != nil {
				// Fall back to SIGKILL if SIGTERM fails (process already dead).
				_ = apacheProcess.Process.Kill()
			}
			// Reap zombie in background; do not block the UI.
			go func(c *exec.Cmd) {
				if c != nil {
					_ = c.Wait()
				}
			}(apacheProcess)
			messages = append(messages, fmt.Sprintf("stopped Apache (PID %d)", pid))
		}
		apacheProcess = nil
	}

	// Fallback A: Apache's own graceful stop, using the generated conf.
	if confPath, err := apacheConfPath(); err == nil {
		if binaryPath, err := apacheBinaryPath(); err == nil {
			_ = exec.Command(binaryPath, "-k", "stop", "-f", confPath).Run()
		}
	}

	// Fallback B: kill the exact PID listening on apachePort (defensive).
	msgs := a.stopApacheOnPort()
	if msgs != "" && !strings.HasPrefix(msgs, "Info:") {
		messages = append(messages, msgs)
	}

	if len(messages) == 0 {
		return "Info: Apache already stopped"
	}
	return strings.Join(messages, "; ")
}

// stopApacheOnPort kills any process listening on the apache port. Used as a
// last-resort cleanup. It never uses process-group kills.
func (a *App) stopApacheOnPort() string {
	out, err := exec.Command("lsof", "-ti", "tcp:"+apachePort, "-sTCP:LISTEN").Output()
	if err != nil {
		return "Info: no Apache process found on port " + apachePort + " (already stopped)"
	}
	pids := strings.TrimSpace(string(out))
	if pids == "" {
		return "Info: no Apache process found on port " + apachePort + " (already stopped)"
	}
	var messages []string
	for _, pid := range strings.Split(pids, "\n") {
		pid = strings.TrimSpace(pid)
		// Safety: never kill PID 0 or negative values.
		if pid == "" || pid == "0" || strings.HasPrefix(pid, "-") {
			continue
		}
		if err := exec.Command("kill", "-TERM", pid).Run(); err != nil {
			messages = append(messages, fmt.Sprintf("failed to kill PID %s: %s", pid, err.Error()))
		} else {
			messages = append(messages, fmt.Sprintf("stopped PID %s", pid))
		}
	}
	if len(messages) == 0 {
		return "Info: no Apache process stopped"
	}
	return strings.Join(messages, "; ")
}
// It uses:
//
//	--datadir=~/.zampp/data/mysql
//	--port=3307  (avoids clashing with XAMPP on 3306)
//	--socket=~/.zampp/data/mysql/mysql.sock  (avoids /tmp/mysql.sock)
//
// The process reference is stored in the package-level mysqlProcess variable.
func (a *App) StartMySQL() string {
	if mysqlProcess != nil && mysqlProcess.Process != nil {
		// Check if the process is still alive by sending signal 0.
		if err := mysqlProcess.Process.Signal(os.Signal(nil)); err == nil {
			return "Info: MySQL is already running"
		}
	}

	binaryPath, err := mysqldPath()
	if err != nil {
		return err.Error()
	}

	// Check that the binary file is present.
	if _, err := os.Stat(binaryPath); err != nil {
		return "MySQL Not Installed — letakkan binary di ~/.zampp/bin/mysql/bin/mysqld"
	}

	// Ensure it is executable.
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Sprintf("Error: cannot make %s executable: %s", binaryPath, err.Error())
	}

	dataDir, err := mysqlDataDir()
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error())
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Sprintf("Error: cannot create data dir %s: %s", dataDir, err.Error())
	}

	socketPath, err := mysqlSocketPath()
	if err != nil {
		return fmt.Sprintf("Error: %s", err.Error())
	}

	// Stop any stale mysqld listening on our port (defensive cleanup).
	a.stopMySQLOnPort()

	cmd := exec.Command(binaryPath,
		"--datadir="+dataDir,
		"--port="+mySQLPort,
		"--socket="+socketPath,
	)
	// Detach stdout/stderr so the process keeps running after the call returns.
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start mysqld: %s", err.Error())
	}

	mysqlProcess = cmd

	// Release the process so it keeps running in the background.
	_ = cmd.Process.Release()

	// Ensure the root account uses username=root / password=root so the
	// frontend (and Adminer) can log in predictably. This runs the mysql
	// client against the just-started server via the Unix socket, and is
	// idempotent — it works on both fresh (no-password) and existing
	// (password-already-set) data dirs.
	go ensureMySQLRootCredentials(socketPath)

	return fmt.Sprintf("Started MySQL on port %s (datadir: %s)", mySQLPort, dataDir)
}

// ensureMySQLRootCredentials guarantees the root MySQL account uses
// password=root. It is run as a goroutine shortly after mysqld starts.
//
// Flow:
//  1. Wait for the Unix socket to appear (mysqld ready).
//  2. Try connecting with password=root. If it works, nothing to do.
//  3. If that fails, try connecting with no password and run
//     `ALTER USER 'root'@'localhost' IDENTIFIED BY 'root';` — this is the
//     fresh-data-dir case.
//  4. If that also fails (root already has a different password), spin up
//     a temporary mysqld with --skip-grant-tables, set root's password to
//     'root' via raw SQL on the `mysql` schema, stop it, and let the main
//     mysqld continue. This guarantees the frontend contract holds even on
//     a previously-used data dir whose root password was changed.
func ensureMySQLRootCredentials(socketPath string) {
	mysqlClient, err := mysqlClientPath()
	if err != nil {
		fmt.Println("mysql: cannot resolve mysql client path:", err)
		return
	}
	if _, err := os.Stat(mysqlClient); err != nil {
		fmt.Println("mysql: client binary not present, skipping root password set")
		return
	}

	// 1) Wait briefly for the socket file to appear.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 2) Try password=root first — if it already works, nothing to do.
	if tryMySQLCommand(mysqlClient, socketPath, "root", "root", "SELECT 1") {
		fmt.Println("mysql: root password already 'root' — nothing to do")
		return
	}

	// 3) Try no password (fresh data dir) -> set root password to 'root'.
	if tryMySQLCommand(mysqlClient, socketPath, "", "", "ALTER USER 'root'@'localhost' IDENTIFIED BY 'root'; FLUSH PRIVILEGES;") {
		fmt.Println("mysql: root password set to 'root' (was empty)")
		return
	}

	// 4) Last resort: skip-grant-tables bootstrap.
	fmt.Println("mysql: falling back to --skip-grant-tables to force root password")
	if err := forceRootPasswordViaGrantTables(); err != nil {
		fmt.Printf("mysql: could not force root password: %s\n", err.Error())
		return
	}
	fmt.Println("mysql: root password forced to 'root' via skip-grant-tables")
}

// tryMySQLCommand runs the mysql client with the given credentials and a
// SQL payload, returning true on success. Empty user means no -u flag and
// empty password means no -p flag (so the client tries password-less login).
// It returns true only if the command exited 0.
func tryMySQLCommand(client, socket, user, pass, sql string) bool {
	args := []string{"-S", socket}
	if user != "" {
		args = append(args, "-u", user)
	}
	if pass != "" {
		args = append(args, "-p"+pass)
	}
	args = append(args, "-e", sql)
	cmd := exec.Command(client, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("mysql: attempt with user=%q pass_set=%v failed: %s: %s\n",
			user, pass != "", err.Error(), strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// forceRootPasswordViaGrantTables starts a temporary mysqld with
// --skip-grant-tables --skip-networking (so anyone can connect without a
// password, but only via the Unix socket, no network exposure), runs SQL to
// set root@localhost's password to 'root', then stops the temporary mysqld.
// The main mysqld continues running unaffected because we use a one-off
// temp socket file.
func forceRootPasswordViaGrantTables() error {
	binaryPath, err := mysqldPath()
	if err != nil {
		return err
	}
	dataDir, err := mysqlDataDir()
	if err != nil {
		return err
	}
	mysqlClient, err := mysqlClientPath()
	if err != nil {
		return err
	}

	// Use a separate socket so the temp mysqld does not conflict with the
	// running main mysqld's socket.
	tempSocket := filepath.Join(dataDir, "grant-reset.sock")
	_ = os.Remove(tempSocket)

	mysqld := exec.Command(binaryPath,
		"--datadir="+dataDir,
		"--socket="+tempSocket,
		"--skip-grant-tables",
		"--skip-networking",
	)
	if err := mysqld.Start(); err != nil {
		return fmt.Errorf("cannot start temp mysqld: %w", err)
	}
	defer func() {
		_ = mysqld.Process.Kill()
		_ = os.Remove(tempSocket)
	}()

	// Wait for temp socket to appear.
	for i := 0; i < 80; i++ {
		if _, err := os.Stat(tempSocket); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(tempSocket); err != nil {
		return fmt.Errorf("temp mysqld socket never appeared")
	}

	// skip-grant-tables means no password is needed AND ALTER USER works.
	sql := "FLUSH PRIVILEGES; ALTER USER 'root'@'localhost' IDENTIFIED BY 'root'; FLUSH PRIVILEGES;"
	cmd := exec.Command(mysqlClient, "-S", tempSocket, "-e", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not set root password via grant tables: %s: %s", err.Error(), strings.TrimSpace(string(out)))
	}
	return nil
}

// mysqlClientPath returns the absolute path to the mysqladmin client binary
// (e.g. ~/.zampp/bin/mysql/bin/mysqladmin).
func mysqlClientPath() (string, error) {
	base, err := mysqlBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bin", "mysqladmin"), nil
}

// StopMySQL stops the running MySQL process, if any.
//
// Safety contract:
//   - Never targets a process group (no `kill -- -PID`, no PID 0/negative).
//   - Primary path: kill the tracked *exec.Cmd's process only.
//   - Fallback A: per-PID `kill -TERM <pid>` for the tracked process.
//   - Fallback B: per-PID `kill -TERM <pid>` for listeners on mySQLPort.
func (a *App) StopMySQL() (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("Error: StopMySQL panic: %v", r)
		}
	}()

	var messages []string

	// Primary path: kill the tracked process only.
	if mysqlProcess != nil && mysqlProcess.Process != nil {
		pid := mysqlProcess.Process.Pid
		if pid > 0 {
			if err := mysqlProcess.Process.Signal(syscall.SIGTERM); err != nil {
				// Per-PID fallback (NOT a process-group kill).
				if err2 := exec.Command("kill", "-TERM", fmt.Sprintf("%d", pid)).Run(); err2 != nil {
					messages = append(messages, fmt.Sprintf("failed to stop MySQL (PID %d): %s", pid, err.Error()))
				} else {
					messages = append(messages, fmt.Sprintf("stopped MySQL (PID %d)", pid))
				}
			} else {
				messages = append(messages, fmt.Sprintf("stopped MySQL (PID %d)", pid))
			}
			// Reap zombie in background.
			go func(c *exec.Cmd) {
				if c != nil {
					_ = c.Wait()
				}
			}(mysqlProcess)
		}
		mysqlProcess = nil
	}

	// Fallback: per-PID cleanup of any listener on mySQLPort.
	msgs := a.stopMySQLOnPort()
	if msgs != "" && !strings.HasPrefix(msgs, "Info:") {
		messages = append(messages, msgs)
	}

	if len(messages) == 0 {
		return "Info: MySQL already stopped"
	}
	return strings.Join(messages, "; ")
}

// stopMySQLOnPort searches for any process listening on tcp:3307 (the port
// reserved for this app's standalone MySQL) and kills it. Used as a defensive
// cleanup when the tracked process is unknown.
func (a *App) stopMySQLOnPort() string {
	out, err := exec.Command("lsof", "-ti", "tcp:"+mySQLPort, "-sTCP:LISTEN").Output()
	if err != nil {
		return "Info: no MySQL process found on port " + mySQLPort + " (already stopped)"
	}

	pids := strings.TrimSpace(string(out))
	if pids == "" {
		return "Info: no MySQL process found on port " + mySQLPort + " (already stopped)"
	}

	var messages []string
	for _, pid := range strings.Split(pids, "\n") {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		// Safety: never kill PID 0 or negative values.
		if pid == "0" || strings.HasPrefix(pid, "-") {
			continue
		}
		if err := exec.Command("kill", "-TERM", pid).Run(); err != nil {
			messages = append(messages, fmt.Sprintf("failed to kill PID %s: %s", pid, err.Error()))
		} else {
			messages = append(messages, fmt.Sprintf("stopped PID %s", pid))
		}
	}

	if len(messages) == 0 {
		return "Info: no MySQL process stopped"
	}
	return strings.Join(messages, "; ")
}
