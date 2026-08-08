import './style.css';
import './app.css';
import zamppLogo from './assets/images/zampp-logo.png';
import {
    StartWebServer,
    StopWebServer,
    StartMySQL,
    StopMySQL,
    OpenAdminer,
    OpenHtdocsFolder,
    OpenWebRoot,
    OpenTerminal,
    CheckFirstRun,
    DownloadAndExtractBinaries,
    CheckPHPVersion,
    DownloadPHPVersion,
} from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

document.querySelector('#app').innerHTML = `
    <div class="app-window">
        <!-- SETUP OVERLAY (first-run downloader) -->
        <div id="setup-screen" class="setup-overlay">
            <div class="setup-card">
                <img class="setup-logo-img" src="${zamppLogo}" alt="ZAMPP">
                <div class="setup-logo">ZAMPP</div>
                <div class="setup-title">Setting up ZAMPP...</div>
                <div class="setup-desc">Downloading server engine, please wait.</div>
                <div class="setup-progress-wrap">
                    <div id="setup-progress-bar" class="setup-progress-bar" style="width:0%"></div>
                </div>
                <div id="setup-percent" class="setup-percent">0%</div>
            </div>
        </div>

        <div class="header">
            <img class="header-logo" src="${zamppLogo}" alt="ZAMPP">
            <div class="header-text">
                <h1>ZAMPP</h1>
                <div class="subtitle">Zero-config Apache MySQL PHP Platform</div>
            </div>
        </div>

        <!-- BARIS 1: WEB SERVER -->
        <div class="service-row">
            <div class="service-info">
                <div class="status-light" id="light-web"></div>
                <div class="details">
                    <div class="service-name">Web Server</div>
                    <div class="controls">
                        <select id="select-engine">
                            <option value="nginx">Nginx</option>
                            <option value="apache">Apache</option>
                        </select>
                        <select id="select-php">
                            <option value="7.3">PHP 7.3</option>
                            <option value="7.4">PHP 7.4</option>
                            <option value="8.0">PHP 8.0</option>
                            <option value="8.1">PHP 8.1</option>
                            <option value="8.2">PHP 8.2</option>
                            <option value="8.3">PHP 8.3</option>
                            <option value="8.4">PHP 8.4</option>
                            <option value="8.5">PHP 8.5</option>
                        </select>
                    </div>
                    <div class="port-info">Port: 8000</div>
                </div>
            </div>
            <button class="btn" id="btn-web">Start</button>
        </div>

        <!-- BARIS 2: MYSQL -->
        <div class="service-row">
            <div class="service-info">
                <div class="status-light" id="light-mysql"></div>
                <div class="details">
                    <div class="service-name">Database</div>
                    <div class="controls">
                        <select id="select-mysql" disabled>
                            <option>MySQL 5.7</option>
                        </select>
                    </div>
                    <div class="port-info">Port: 3307 • User: root • Pass: root</div>
                </div>
            </div>
            <button class="btn" id="btn-mysql">Start</button>
        </div>

        <!-- TOOLBAR -->
        <div class="toolbar">
            <button class="btn btn-tool" id="btn-webroot">WebRoot</button>
            <button class="btn btn-tool" id="btn-adminer">Adminer</button>
            <button class="btn btn-tool" id="btn-terminal">💻 Terminal</button>
            <button class="btn btn-tool" id="btn-htdocs">📁 Open htdocs</button>
        </div>

        <!-- TOAST -->
        <div class="toast" id="toast"></div>
    </div>
`;

// ===== Element refs =====
const engineSelect = document.getElementById('select-engine');
const phpSelect = document.getElementById('select-php');
const webBtn = document.getElementById('btn-web');
const webLight = document.getElementById('light-web');

const mysqlBtn = document.getElementById('btn-mysql');
const mysqlLight = document.getElementById('light-mysql');

const webrootBtn = document.getElementById('btn-webroot');
const adminerBtn = document.getElementById('btn-adminer');
const htdocsBtn = document.getElementById('btn-htdocs');
const terminalBtn = document.getElementById('btn-terminal');

const toastEl = document.getElementById('toast');

// ===== State =====
let isWebRunning = false;
let isMySQLRunning = false;
let webEngine = 'nginx'; // engine locked at start time
let needsPHPDownload = false; // selected PHP version not installed yet
let isPHPDownloading = false; // per-version PHP download in progress
let pendingPHPVersion = null; // version queued to auto-start after download

function showToast(text, isError) {
    toastEl.textContent = text;
    toastEl.className = 'toast show' + (isError ? ' error' : '');
}

function setWebRunning(running) {
    isWebRunning = running;
    if (running) {
        webLight.classList.add('on');
        webBtn.innerText = 'Stop';
        webBtn.classList.add('stop-btn');
        webBtn.classList.remove('download-btn');
        engineSelect.disabled = true;
        phpSelect.disabled = true;
    } else {
        webLight.classList.remove('on');
        // Restore button label based on whether selected PHP needs download.
        if (needsPHPDownload) {
            webBtn.innerText = '⬇️ Download PHP ' + phpSelect.value;
            webBtn.classList.add('download-btn');
            webBtn.classList.remove('stop-btn');
        } else {
            webBtn.innerText = 'Start';
            webBtn.classList.remove('stop-btn');
            webBtn.classList.remove('download-btn');
        }
        engineSelect.disabled = false;
        phpSelect.disabled = false;
    }
}

// checkSelectedPHPVersion queries Go whether the selected PHP version is
// installed and updates the main button state accordingly. PHP 7.4 ships
// with the base engine, so it is always considered installed; every other
// version (7.3, 8.0 - 8.5) is modular and checked on demand.
function checkSelectedPHPVersion() {
    if (isWebRunning) return; // do not flip the Stop button while running
    const version = phpSelect.value;
    if (version === '7.4') {
        needsPHPDownload = false;
        webBtn.innerText = 'Start';
        webBtn.classList.remove('download-btn');
        return;
    }
    CheckPHPVersion(version)
        .then((installed) => {
            if (isWebRunning) return; // state changed while awaiting
            needsPHPDownload = !installed;
            if (needsPHPDownload) {
                webBtn.innerText = '⬇️ Download PHP ' + version;
                webBtn.classList.add('download-btn');
            } else {
                webBtn.innerText = 'Start';
                webBtn.classList.remove('download-btn');
            }
        })
        .catch(() => {
            // On error, fall back to Start label.
            needsPHPDownload = false;
            webBtn.innerText = 'Start';
            webBtn.classList.remove('download-btn');
        });
}

// showSetupOverlay(title, desc) displays the first-run-style overlay for
// either the base engine download or a per-version PHP download.
function showSetupOverlay(title, desc) {
    if (!setupScreen) return;
    const titleEl = setupScreen.querySelector('.setup-title');
    const descEl = setupScreen.querySelector('.setup-desc');
    if (titleEl) titleEl.textContent = title;
    if (descEl) descEl.textContent = desc;
    if (setupBar) setupBar.style.width = '0%';
    if (setupPct) setupPct.textContent = '0%';
    setupScreen.classList.remove('fade-out');
    setupScreen.style.display = 'flex';
}

function hideSetupOverlay() {
    if (!setupScreen) return;
    setupScreen.classList.add('fade-out');
    setTimeout(() => {
        if (setupScreen) setupScreen.style.display = 'none';
    }, 600);
}

function setMySQLRunning(running) {
    isMySQLRunning = running;
    if (running) {
        mysqlLight.classList.add('on');
        mysqlBtn.innerText = 'Stop';
        mysqlBtn.classList.add('stop-btn');
    } else {
        mysqlLight.classList.remove('on');
        mysqlBtn.innerText = 'Start';
        mysqlBtn.classList.remove('stop-btn');
    }
}

// ===== Web Server toggle =====
// The main button has 3 states: Start, Stop (running), or Download PHP.
// toggleWeb inspects current state and routes to the correct action.
function toggleWeb() {
    if (isWebRunning) {
        stopWeb();
        return;
    }
    if (isPHPDownloading) {
        // A download is already in progress; ignore extra clicks.
        return;
    }
    if (needsPHPDownload) {
        downloadSelectedPHP();
        return;
    }
    startWeb();
}

// downloadSelectedPHP triggers the per-version PHP downloader for the
// currently selected version: shows the overlay, calls Go DownloadPHPVersion,
// listens for php-download-progress / php-download-complete, then auto-starts
// the web server once the version lands.
function downloadSelectedPHP() {
    const version = phpSelect.value;
    if (!version || version === '7.4') return;
    isPHPDownloading = true;
    pendingPHPVersion = version;
    webBtn.disabled = true;
    phpSelect.disabled = true;
    engineSelect.disabled = true;
    showSetupOverlay('Downloading PHP ' + version + '...', 'Fetching PHP ' + version + ' module, please wait.');

    DownloadPHPVersion(version).catch((err) => {
        const msg = typeof err === 'string' ? err : String(err);
        if (setupPct) setupPct.textContent = 'Error: ' + msg;
        isPHPDownloading = false;
        webBtn.disabled = false;
        phpSelect.disabled = false;
        engineSelect.disabled = false;
    });
}

// onPHPDownloadComplete runs when 'php-download-complete' fires: hides the
// overlay, restores controls, marks the version as installed, and (if the
// user originally clicked the download button) auto-starts the web server.
function onPHPDownloadComplete() {
    hideSetupOverlay();
    isPHPDownloading = false;
    webBtn.disabled = false;
    needsPHPDownload = false;
    pendingPHPVersion = null;

    // Re-evaluate button label (should now show Start for the freshly
    // installed version, then auto-start the server).
    checkSelectedPHPVersion();

    // Auto-start the web server using the freshly downloaded version.
    startWeb();
}

function startWeb() {
    const engine = engineSelect.value;
    const php = phpSelect.value;
    webEngine = engine;
    setWebRunning(true);
    showToast('Starting ' + engine + ' + PHP ' + php + '...', false);

    StartWebServer(engine, php)
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
                setWebRunning(false);
            } else {
                showToast(result, false);
            }
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
            setWebRunning(false);
        });
}

function stopWeb() {
    showToast('Stopping ' + webEngine + ' + PHP...', false);

    StopWebServer(webEngine)
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
            } else {
                showToast(result, false);
            }
            setWebRunning(false);
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
            setWebRunning(false);
        });
}

// ===== MySQL toggle =====
function toggleMySQL() {
    if (isMySQLRunning) {
        stopMySQL();
    } else {
        startMySQL();
    }
}

function startMySQL() {
    setMySQLRunning(true);
    showToast('Starting MySQL...', false);

    StartMySQL()
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
                setMySQLRunning(false);
            } else {
                showToast(result, false);
            }
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
            setMySQLRunning(false);
        });
}

function stopMySQL() {
    showToast('Stopping MySQL...', false);

    StopMySQL()
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
            } else {
                showToast(result, false);
            }
            setMySQLRunning(false);
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
            setMySQLRunning(false);
        });
}

// ===== Toolbar handlers =====
webrootBtn.addEventListener('click', () => {
    if (!isWebRunning) {
        showToast('Jalankan Web Server terlebih dahulu.', true);
        return;
    }
    OpenWebRoot()
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
            } else {
                showToast(result, false);
            }
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
        });
});

adminerBtn.addEventListener('click', () => {
    if (!isWebRunning) {
        showToast('Jalankan Web Server terlebih dahulu.', true);
        return;
    }

    OpenAdminer()
        .then((result) => {
            if (result && result.indexOf('Error') === 0) {
                showToast(result, true);
            } else {
                showToast(result, false);
            }
        })
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
        });
});

htdocsBtn.addEventListener('click', () => {
    OpenHtdocsFolder()
        .then(() => showToast('Opened htdocs folder in Finder.', false))
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
        });
});

// ===== Terminal handler =====
// Opens a macOS Terminal session with the ZAMPP PHP binary for the
// currently selected PHP version first on PATH, plus a `composer` alias
// pointing to the (auto-downloaded) composer.phar. Composer is fetched
// on-demand by the Go backend on first open.
terminalBtn.addEventListener('click', () => {
    const version = phpSelect.value;
    if (!version) {
        showToast('Pilih versi PHP terlebih dahulu.', true);
        return;
    }
    if (needsPHPDownload) {
        showToast('PHP ' + version + ' belum terpasang. Klik tombol utama untuk download.', true);
        return;
    }
    showToast('Membuka Terminal dengan PHP ' + version + '...', false);
    OpenTerminal(version)
        .then(() => showToast('Terminal siap dengan PHP ' + version + ' & Composer.', false))
        .catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            showToast(msg, true);
        });
});

// ===== Bindings =====
webBtn.addEventListener('click', toggleWeb);
mysqlBtn.addEventListener('click', toggleMySQL);
phpSelect.addEventListener('change', checkSelectedPHPVersion);

// ===== Initial render =====
setWebRunning(false);
setMySQLRunning(false);
checkSelectedPHPVersion();

// ===== First-run setup flow =====
const setupScreen = document.getElementById('setup-screen');
const setupBar = document.getElementById('setup-progress-bar');
const setupPct = document.getElementById('setup-percent');

// Listen for download progress events from the Go backend.
EventsOn('download-progress', (pct) => {
    const value = Math.max(0, Math.min(100, Number(pct) || 0));
    if (setupBar) setupBar.style.width = value + '%';
    if (setupPct) setupPct.textContent = value + '%';
});

// Listen for completion; fade out overlay then hide.
EventsOn('download-complete', () => {
    if (setupBar) setupBar.style.width = '100%';
    if (setupPct) setupPct.textContent = '100%';
    if (!setupScreen) return;
    setupScreen.classList.add('fade-out');
    setTimeout(() => {
        if (setupScreen) setupScreen.style.display = 'none';
    }, 600);
});

// Per-version PHP download progress.
EventsOn('php-download-progress', (pct) => {
    const value = Math.max(0, Math.min(100, Number(pct) || 0));
    if (setupBar) setupBar.style.width = value + '%';
    if (setupPct) setupPct.textContent = value + '%';
});

// Per-version PHP download complete: hide overlay, restore controls, auto-start.
EventsOn('php-download-complete', onPHPDownloadComplete);

// On load, check if binaries are already installed.
CheckFirstRun()
    .then((installed) => {
        if (installed) {
            // Already set up — hide overlay immediately.
            if (setupScreen) setupScreen.style.display = 'none';
            return;
        }
        // First run — keep overlay visible and start the download.
        DownloadAndExtractBinaries().catch((err) => {
            const msg = typeof err === 'string' ? err : String(err);
            if (setupPct) setupPct.textContent = 'Error: ' + msg;
        });
    })
    .catch((err) => {
        const msg = typeof err === 'string' ? err : String(err);
        if (setupPct) setupPct.textContent = 'Error: ' + msg;
    });
