package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
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
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
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
const phpInternalPort = "8081"

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

// StartPHP starts the PHP built-in web server for the given version.
// version is the folder name under ~/.zampp/bin/php (e.g. "8.2").
// The server listens on localhost:8081 (internal) and serves from
// ~/.zampp/htdocs. Nginx (on :8000) proxies .php requests here.
func (a *App) StartPHP(version string) string {
	if version == "" {
		return "Error: PHP version is empty"
	}

	binaryPath, err := getPHPExecutablePath(version)
	if err != nil {
		// err already carries the "belum terpasang" message.
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

	cmd := exec.Command(binaryPath, "-S", "127.0.0.1:"+phpInternalPort, "-t", docRoot)
	// Detach from stdout/stderr so the process keeps running after the call returns.
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start PHP %s: %s", version, err.Error())
	}

	phpProcess = cmd

	// Release the process so it keeps running in the background.
	_ = cmd.Process.Release()

	return fmt.Sprintf("Started PHP %s on http://127.0.0.1:%s (docroot: %s)", version, phpInternalPort, docRoot)
}

// StopPHP stops the PHP process currently listening on the internal port (8081).
func (a *App) StopPHP() string {
	// Primary path: kill the tracked PHP process if we still hold a reference
	// to it. Never target a process group (no -PID, no PID 0 or negative).
	if phpProcess != nil && phpProcess.Process != nil {
		pid := phpProcess.Process.Pid
		if pid > 0 {
			if err := phpProcess.Process.Kill(); err != nil {
				// Per-PID fallback (NOT a process-group kill).
				_ = exec.Command("kill", "-TERM", fmt.Sprintf("%d", pid)).Run()
			}
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

	accessLog := filepath.Join(logDir, "access.log")
	errorLog := filepath.Join(logDir, "error.log")
	pidFile := filepath.Join(logDir, "nginx.pid")

	// Template uses tabs indentation; written via raw string. Paths are inserted
	// as-is (they are already absolute and shell-safe inside nginx.conf).
	conf := "worker_processes  1;\n" +
		"pid " + pidFile + ";\n\n" +
		"events {\n" +
		"    worker_connections  1024;\n" +
		"}\n\n" +
		"http {\n" +
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
		"        location ~ \\.php$ {\n" +
		"            proxy_pass http://127.0.0.1:" + phpInternalPort + ";\n" +
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
	return nil
}

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

	cmd := exec.Command(binaryPath, "-c", confPath)
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start nginx: %s", err.Error())
	}

	nginxProcess = cmd
	_ = cmd.Process.Release()

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
func (a *App) StopNginx() string {
	var messages []string

	// Primary path: kill the tracked process only.
	if nginxProcess != nil && nginxProcess.Process != nil {
		pid := nginxProcess.Process.Pid
		if pid > 0 {
			if err := nginxProcess.Process.Kill(); err != nil {
				messages = append(messages, fmt.Sprintf("Kill() failed for PID %d: %s", pid, err.Error()))
			} else {
				messages = append(messages, fmt.Sprintf("stopped Nginx (PID %d)", pid))
			}
		}
		nginxProcess = nil
	}

	// Fallback A: nginx's own graceful stop, using the generated conf.
	if confPath, err := nginxConfPath(); err == nil {
		if binaryPath, err := nginxBinaryPath(); err == nil {
			_ = exec.Command(binaryPath, "-s", "stop", "-c", confPath).Run()
		}
	}

	// Fallback B: kill any process whose command line matches "nginx". This is
	// process-name based, not PID based, so it never targets a process group.
	_ = exec.Command("pkill", "-f", "nginx").Run()

	// Fallback C: as a last resort, kill the exact PID listening on nginxPort.
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

// OpenAdminer opens Adminer in the user's default browser via the macOS
// `open` command. It routes the URL through whichever web server is currently
// running (preferring Nginx :8000, falling back to Apache :8000), and prefills
// the MySQL server field to 127.0.0.1:<mySQLPort> so login works against the
// app's standalone MySQL on port 3307 (not XAMPP's 3306).
func (a *App) OpenAdminer() string {
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

	// Adminer accepts ?server= and ?username= as GET params. We set the server
	// to 127.0.0.1:3307 so the user only needs to type the password.
	url := baseURL + "/adminer.php?server=127.0.0.1:" + mySQLPort + "&username=root"

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

// StartWebServer starts the PHP built-in server (internal port 8081) and then
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
	b.WriteString("ServerRoot \"" + serverRoot + "\"\n")
	b.WriteString("Listen " + apachePort + "\n\n")
	b.WriteString("LoadModule unixd_module modules/mod_unixd.so\n")
	b.WriteString("LoadModule authz_core_module modules/mod_authz_core.so\n")
	b.WriteString("LoadModule dir_module modules/mod_dir.so\n")
	b.WriteString("LoadModule mime_module modules/mod_mime.so\n")
	b.WriteString("LoadModule rewrite_module modules/mod_rewrite.so\n")
	b.WriteString("LoadModule proxy_module modules/mod_proxy.so\n")
	b.WriteString("LoadModule proxy_http_module modules/mod_proxy_http.so\n\n")
	b.WriteString("ServerName localhost\n")
	b.WriteString("PidFile \"" + pidFile + "\"\n\n")
	b.WriteString("DocumentRoot \"" + docRoot + "\"\n")
	b.WriteString("<Directory \"" + docRoot + "\">\n")
	b.WriteString("    Options Indexes FollowSymLinks\n")
	b.WriteString("    AllowOverride All\n")
	b.WriteString("    Require all granted\n")
	b.WriteString("</Directory>\n\n")
	b.WriteString("DirectoryIndex index.php index.html\n")
	b.WriteString("TypesConfig conf/mime.types\n\n")
	b.WriteString("ProxyPassMatch ^/(.*\\.php(/.*)?)$ http://127.0.0.1:" + phpInternalPort + "/$1\n")

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

	cmd := exec.Command(binaryPath, "-f", confPath)
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("Error: failed to start apache: %s", err.Error())
  }

	apacheProcess = cmd
	_ = cmd.Process.Release()

	return fmt.Sprintf("Started Apache on port %s (conf: %s)", apachePort, confPath)
}

// StopApache stops the running httpd process, if any.
//
// Safety contract:
//   - Never targets a process group (no `kill -- -PID`, no PID 0/negative).
//   - Primary path: kill the tracked *exec.Cmd's process only.
//   - Fallback A: `httpd -k stop -f <conf>` (Apache's own graceful stop).
//   - Fallback B: `pkill -f httpd` (matches the httpd command line, no PID).
//   - Fallback C: per-PID `kill -TERM <pid>` for listeners on apachePort.
func (a *App) StopApache() string {
	var messages []string

	// Primary path: kill the tracked process only.
	if apacheProcess != nil && apacheProcess.Process != nil {
		pid := apacheProcess.Process.Pid
		if pid > 0 {
			if err := apacheProcess.Process.Kill(); err != nil {
				messages = append(messages, fmt.Sprintf("Kill() failed for PID %d: %s", pid, err.Error()))
			} else {
				messages = append(messages, fmt.Sprintf("stopped Apache (PID %d)", pid))
			}
		}
		apacheProcess = nil
	}

	// Fallback A: Apache's own graceful stop, using the generated conf.
	if confPath, err := apacheConfPath(); err == nil {
		if binaryPath, err := apacheBinaryPath(); err == nil {
			_ = exec.Command(binaryPath, "-k", "stop", "-f", confPath).Run()
		}
	}

	// Fallback B: kill any process whose command line matches "httpd". This is
	// process-name based, not PID based, so it never targets a process group.
	_ = exec.Command("pkill", "-f", "httpd").Run()

	// Fallback C: as a last resort, kill the exact PID listening on apachePort.
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

	return fmt.Sprintf("Started MySQL on port %s (datadir: %s)", mySQLPort, dataDir)
}

// StopMySQL stops the running MySQL process, if any.
//
// Safety contract:
//   - Never targets a process group (no `kill -- -PID`, no PID 0/negative).
//   - Primary path: kill the tracked *exec.Cmd's process only.
//   - Fallback A: per-PID `kill -TERM <pid>` for the tracked process.
//   - Fallback B: per-PID `kill -TERM <pid>` for listeners on mySQLPort.
func (a *App) StopMySQL() string {
	var messages []string

	// Primary path: kill the tracked process only.
	if mysqlProcess != nil && mysqlProcess.Process != nil {
		pid := mysqlProcess.Process.Pid
		if pid > 0 {
			if err := mysqlProcess.Process.Kill(); err != nil {
				// Per-PID fallback (NOT a process-group kill).
				if err2 := exec.Command("kill", "-TERM", fmt.Sprintf("%d", pid)).Run(); err2 != nil {
					messages = append(messages, fmt.Sprintf("failed to stop MySQL (PID %d): %s", pid, err.Error()))
				} else {
					messages = append(messages, fmt.Sprintf("stopped MySQL (PID %d)", pid))
				}
			} else {
				messages = append(messages, fmt.Sprintf("stopped MySQL (PID %d)", pid))
			}
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
