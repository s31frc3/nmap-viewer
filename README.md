# Nmap XML Viewer

Web UI for viewing Nmap XML results, grouping by host/port/service, rescanning selected ports, and annotating findings.

## Features
1. Upload Nmap XML files and aggregate results.
2. Group by `Service`, `Host`, or `Port`.
3. Filter by `State`.
4. Select rows or whole groups and rescan selected ports.
5. Rescan jobs with live logs, diffs, and notifications (toast).
6. Copy `host:port` for a row, a group, or all results.
7. Clear all scans to reload fresh data.
8. Per‑row annotations: color mark + comment (saved in DB).
9. Job history persists in the browser (localStorage).

## Requirements
1. Go 1.20+
2. Nmap in `PATH` (only required for `Rescan`)
## Install
```bash
git clone https://github.com/s31frc3/nmap-viewer
cd nmap_web
```
## Run
```bash
go run . -db ./nmap_viewer.db.json
```

Then open:
```
http://localhost:8080
```

## Usage
1. Upload one or more Nmap XML files.
1. Group and filter as needed.
1. Select rows or groups and click `Rescan Selected`.
1. Use `Rescan Jobs (N)` to open job history and details.
1. Click a row to open details, set a color mark, and leave a comment.
1. Use `Copy All Hosts` or per‑row copy to export `host:port`.

## Import Rules
1. TCP import: `open` and `filtered`
1. UDP import: `open` only

## Notes
1. Annotations are stored in the JSON DB.
1. Job history is stored in the browser `localStorage` (per browser).

## Flags
1. `-addr` listen address (default `:8080`)
1. `-db` path to JSON database file

