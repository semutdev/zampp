<p align="center">
  <img src="img/zampp-logo.png" alt="ZAMPP Logo" width="230">
</p>

<h1 align="center">ZAMPP</h1>
<p align="center">
  <img alt="platform" src="https://img.shields.io/badge/platform-macOS-lightgrey">
  <img alt="macos" src="https://img.shields.io/badge/macOS-Catalina%2010.15%2B-blue">
</p>

<p align="center">Zero-config Apache MySQL PHP Platform</p>

ZAMPP is a Native Desktop application for macOS that serves as a Local Web Development Environment. It is designed to be a much lighter, faster, and fully *Zero-Configuration* alternative to MAMP/XAMPP.

No more dealing with complex port setups or memory-heavy applications. Just one click, and your local server is up and running.

## Key Features
- **Zero-config:** Just click "Start" and start coding. No configuration file headaches.
- **Native macOS UI:** Built with Golang and Wails, delivering a clean, lightweight interface that blends perfectly with the macOS environment.
- **Modular Architecture:** The main installer is incredibly small! Additional PHP versions (7.4 - 8.5) can be downloaded directly from within the app only when you need them (*On-Demand Download*).
- **Multi PHP Version** ZAMPP can switch multi php Version
- **Fast Startup:** Nginx/Apache, MySQL, and PHP engines run independently in the background at maximum speed.
- **Terminal Smart** Auto switch php and composer direct to terminal

---

## Installation Guide

1. Visit the [**Releases**](https://github.com/semutdev/zampp/releases/latest) page.
2. Download For Mac the [**ZAMPP-macOS.dmg**](https://github.com/semutdev/zampp/releases/download/v1.0.0/ZAMPP-macOS.dmg) file (Do not download the engine zip files manually).
3. Open the downloaded `.dmg` file and drag **`ZAMPP.app`** into your `Applications` folder.
4. Launch the ZAMPP app. *(On the first run, the app will automatically download the base server engines in the background).*

> **⚠️ Note for Mac Users:**
> Since this application does not yet have an Apple Developer certificate (Unsigned), macOS Gatekeeper might block it on the first launch.
> **Solution:** Right-click (or Control-click) on the ZAMPP app in your Applications folder -> Select **Open** -> Click **Open** again on the warning prompt.

---

## Screenshot

<p align="center">
  <img src="img/zampp-screenshoot.png" alt="ZAMPP Screenshot" width="620">
</p>

---

## Directory Structure (htdocs)

All your website project files (HTML/PHP/WordPress) should be placed inside the *Document Root* folder. You can open this directory directly by clicking the **"📁 Open htdocs"** button in the ZAMPP app, or access it manually at:

```
~/.zampp/
  ├── bin/        binaries (php, mysql, nginx, apache)
  ├── conf/       generated configs (nginx.conf, httpd.conf)
  ├── data/mysql/ MySQL data + socket
  ├── logs/       per-engine logs
  └── htdocs/     document root
```

---

## Ports & Defaults

| Service        | Port | Notes                                      |
|----------------|------|--------------------------------------------|
| Web Server     | 8000 | Nginx or Apache (user-selectable)         |
| MySQL          | 3307 | Avoids clashing with XAMPP's 3306         |
| PHP (internal) | 8001 | Proxied through the chosen web server      |

Zero manual configuration — config is regenerated on each start.

---

## First-Run Engine Bundle & Modular PHP

On first launch, ZAMPP downloads the base engine bundle (which ships **PHP 7.4**) from GitHub Releases and extracts it into `~/.zampp`:

```
https://github.com/semutdev/zampp/releases/download/v1.0.0/zampp-mac-x64-v1.zip
```

Additional PHP versions (7.4 - 8.5) are **modular** — downloaded on-demand directly from the app when you select a version that isn't installed yet:

```
https://github.com/semutdev/zampp/releases/download/v1.0.0/php-{version}.zip
```

Progress for both flows is reported to the frontend via events emitted from Go:

- `download-progress` / `php-download-progress` — payload `0`–`100`
- `download-complete` / `php-download-complete` — emitted after extraction and cleanup

---

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.

You can configure the project by editing `wails.json`. More information about the project settings can be found here: https://wails.io/docs/reference/project-config

---

<p align="center">
  Built with ❤️ by <a href="https://semut.dev">semut.dev</a>
</p>

