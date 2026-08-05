<p align="center">
  <img src="img/zampp-logo.png" alt="ZAMPP Logo" width="200">
</p>

<h1 align="center">ZAMPP</h1>

<p align="center">Zero-config Apache MySQL PHP Platform</p>

<p align="center">
  <img alt="platform" src="https://img.shields.io/badge/platform-macOS-lightgrey">
  <img alt="macos" src="https://img.shields.io/badge/macOS-Catalina%2010.15%2B-blue">
</p>

## About

ZAMPP is a zero-config desktop platform for running Apache, NGINX, MySQL, and PHP — built with [Wails](https://wails.io) (Go backend + Vanilla JS frontend).

> **Platform support (current):** macOS only — Catalina (10.15) or newer. Apple Silicon and Intel both supported. Windows and Linux variants are planned, but not yet shipped.

On first launch, ZAMPP automatically downloads and extracts its server engine bundle (**binaries + configs**) from GitHub Releases into the user's home directory — no manual setup required.

## Features

- **Web Server** (Apache or NGINX) on port **8000**
- **PHP** built-in server on internal port **8081**, proxied through the chosen web server
- **MySQL** on port **3307** (avoids clashing with XAMPP's 3306)
- **Adminer** integration via the running web server
- **One-click htdocs** folder opener (Finder)
- **First-run downloader** — fetches the engine ZIP from GitHub and extracts it into `~/.zampp`
- Zero manual configuration — config is regenerated on each start

## Project Layout (after setup)

```
~/.zampp/
  ├── bin/        binaries (php, mysql, nginx, apache)
  ├── conf/       generated configs (nginx.conf, httpd.conf)
  ├── data/mysql/ MySQL data + socket
  ├── logs/       per-engine logs
  └── htdocs/     document root
```

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.

## First-Run Engine Bundle

The downloader fetches the engine ZIP from:

```
https://github.com/semutdev/zampp/releases/download/v1.0.0/zampp-mac-x64-v1.zip
```

The archive already contains the `.zampp/` root folder, so extraction into the home directory (`~`) lands at:

- `~/.zampp/bin`
- `~/.zampp/conf`

Progress for the entire flow is reported to the frontend via two events emitted from Go:

- `download-progress` — payload `0`–`100`
- `download-complete` — emitted after extraction and cleanup

## Configuration

You can configure the project by editing `wails.json`. More information about the project settings can be found here: https://wails.io/docs/reference/project-config
