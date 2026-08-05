import './style.css';
import './app.css';
import {
    StartWebServer,
    StopWebServer,
    StartMySQL,
    StopMySQL,
    OpenAdminer,
    OpenHtdocsFolder,
    OpenWebRoot,
} from '../wailsjs/go/main/App';

document.querySelector('#app').innerHTML = `
    <div class="app-window">
        <div class="header">
            <h1>ZAMPP</h1>
            <div class="subtitle">Zero-config Apache MySQL PHP Platform</div>
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
                    <div class="port-info">Port: 3307</div>
                </div>
            </div>
            <button class="btn" id="btn-mysql">Start</button>
        </div>

        <!-- TOOLBAR -->
        <div class="toolbar">
            <button class="btn btn-tool" id="btn-webroot">WebRoot</button>
            <button class="btn btn-tool" id="btn-adminer">Adminer</button>
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

const toastEl = document.getElementById('toast');

// ===== State =====
let isWebRunning = false;
let isMySQLRunning = false;
let webEngine = 'nginx'; // engine locked at start time

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
        engineSelect.disabled = true;
        phpSelect.disabled = true;
    } else {
        webLight.classList.remove('on');
        webBtn.innerText = 'Start';
        webBtn.classList.remove('stop-btn');
        engineSelect.disabled = false;
        phpSelect.disabled = false;
    }
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
function toggleWeb() {
    if (isWebRunning) {
        stopWeb();
    } else {
        startWeb();
    }
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

// ===== Bindings =====
webBtn.addEventListener('click', toggleWeb);
mysqlBtn.addEventListener('click', toggleMySQL);

// ===== Initial render =====
setWebRunning(false);
setMySQLRunning(false);
