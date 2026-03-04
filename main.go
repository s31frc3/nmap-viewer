package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type PortInfo struct {
	Host        string    `json:"host"`
	Port        string    `json:"port"`
	Proto       string    `json:"proto"`
	State       string    `json:"state"`
	Service     string    `json:"service"`
	Product     string    `json:"product"`
	Version     string    `json:"version"`
	ExtraInfo   string    `json:"extrainfo"`
	LastUpdated time.Time `json:"lastUpdated"`
	Source      string    `json:"source"`
	Color       string    `json:"color"`
	Comment     string    `json:"comment"`
}

type Store struct {
	mu          sync.RWMutex
	items       map[string]*PortInfo
	rescanCount int
}

func NewStore() *Store {
	return &Store{items: make(map[string]*PortInfo)}
}

func key(host, port, proto string) string {
	return host + "|" + port + "|" + proto
}

func (s *Store) Upsert(pi PortInfo, force bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(pi.Host, pi.Port, pi.Proto)
	if existing, ok := s.items[k]; ok {
		if force {
			if pi.Color == "" {
				pi.Color = existing.Color
			}
			if pi.Comment == "" {
				pi.Comment = existing.Comment
			}
			*existing = pi
			return
		}
		if pi.State != "" {
			if existing.State == "" || stateRank(pi.State) > stateRank(existing.State) {
				existing.State = pi.State
			} else if stateRank(pi.State) == stateRank(existing.State) && pi.LastUpdated.After(existing.LastUpdated) {
				existing.State = pi.State
			}
		}
		if existing.Service == "" && pi.Service != "" {
			existing.Service = pi.Service
		}
		if existing.Product == "" && pi.Product != "" {
			existing.Product = pi.Product
		}
		if existing.Version == "" && pi.Version != "" {
			existing.Version = pi.Version
		}
		if existing.ExtraInfo == "" && pi.ExtraInfo != "" {
			existing.ExtraInfo = pi.ExtraInfo
		}
		if pi.LastUpdated.After(existing.LastUpdated) {
			existing.LastUpdated = pi.LastUpdated
			existing.Source = pi.Source
		}
		if existing.Color == "" && pi.Color != "" {
			existing.Color = pi.Color
		}
		if existing.Comment == "" && pi.Comment != "" {
			existing.Comment = pi.Comment
		}
		return
	}
	s.items[k] = &pi
}

func (s *Store) Touch(host, port, proto, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(host, port, proto)
	if existing, ok := s.items[k]; ok {
		existing.LastUpdated = time.Now()
		existing.Source = source
	}
}

func (s *Store) Get(host, port, proto string) (PortInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := key(host, port, proto)
	if v, ok := s.items[k]; ok {
		return *v, true
	}
	return PortInfo{}, false
}

func (s *Store) DeleteHost(host string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for k, v := range s.items {
		if v.Host == host {
			delete(s.items, k)
			deleted++
		}
	}
	return deleted
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*PortInfo)
}

func (s *Store) IncRescanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rescanCount++
	return s.rescanCount
}

func (s *Store) RescanCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rescanCount
}

func (s *Store) UpdateAnnotation(host, port, proto, color, comment string) (PortInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(host, port, proto)
	if existing, ok := s.items[k]; ok {
		existing.Color = color
		existing.Comment = comment
		return *existing, true
	}
	return PortInfo{}, false
}

func (s *Store) List() []PortInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PortInfo, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

func (s *Store) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	type dbWrapper struct {
		Items       []PortInfo `json:"items"`
		RescanCount int        `json:"rescanCount"`
	}
	var items []PortInfo
	var count int
	trim := strings.TrimSpace(string(b))
	if len(trim) > 0 && trim[0] == '[' {
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
	} else {
		var wrap dbWrapper
		if err := json.Unmarshal(b, &wrap); err != nil {
			return err
		}
		items = wrap.Items
		count = wrap.RescanCount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*PortInfo, len(items))
	for _, it := range items {
		clone := it
		s.items[key(it.Host, it.Port, it.Proto)] = &clone
	}
	s.rescanCount = count
	return nil
}

func (s *Store) Save(path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]PortInfo, 0, len(s.items))
	for _, v := range s.items {
		items = append(items, *v)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Service != items[j].Service {
			return items[i].Service < items[j].Service
		}
		if items[i].Host != items[j].Host {
			return items[i].Host < items[j].Host
		}
		if items[i].Port != items[j].Port {
			return items[i].Port < items[j].Port
		}
		return items[i].Proto < items[j].Proto
	})
	wrap := struct {
		Items       []PortInfo `json:"items"`
		RescanCount int        `json:"rescanCount"`
	}{
		Items:       items,
		RescanCount: s.rescanCount,
	}
	b, err := json.MarshalIndent(wrap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Target struct {
	Host  string `json:"host"`
	Port  string `json:"port"`
	Proto string `json:"proto"`
}

type Diff struct {
	Host   string   `json:"host"`
	Port   string   `json:"port"`
	Proto  string   `json:"proto"`
	Before PortInfo `json:"before"`
	After  PortInfo `json:"after"`
}

type ScanJob struct {
	mu       sync.Mutex
	id       string
	done     bool
	message  string
	logs     []string
	diff     []Diff
	watchers map[chan string]struct{}
}

func (j *ScanJob) addLog(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.logs = append(j.logs, line)
	for ch := range j.watchers {
		select {
		case ch <- line:
		default:
		}
	}
}

func (j *ScanJob) setDone(msg string) {
	j.mu.Lock()
	j.done = true
	j.message = msg
	for ch := range j.watchers {
		close(ch)
	}
	j.watchers = map[chan string]struct{}{}
	j.mu.Unlock()
}

func (j *ScanJob) Done() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

func (j *ScanJob) Message() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.message
}

func (j *ScanJob) Logs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, len(j.logs))
	copy(out, j.logs)
	return out
}

func (j *ScanJob) Diffs() []Diff {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Diff, len(j.diff))
	copy(out, j.diff)
	return out
}

func (j *ScanJob) Subscribe() chan string {
	j.mu.Lock()
	defer j.mu.Unlock()
	ch := make(chan string, 50)
	j.watchers[ch] = struct{}{}
	if j.done {
		close(ch)
	}
	return ch
}

func (j *ScanJob) Unsubscribe(ch chan string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.watchers, ch)
}

type ScanManager struct {
	mu     sync.Mutex
	jobs   map[string]*ScanJob
	store  *Store
	seq    int64
	onSave func()
}

func NewScanManager(store *Store, onSave func()) *ScanManager {
	return &ScanManager{jobs: make(map[string]*ScanJob), store: store, onSave: onSave}
}

func (m *ScanManager) Start(targets []Target) (string, error) {
	if len(targets) == 0 {
		return "", errors.New("no targets")
	}
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("job-%d-%d", time.Now().Unix(), m.seq)
	job := &ScanJob{id: id, watchers: map[chan string]struct{}{}}
	m.jobs[id] = job
	m.mu.Unlock()

	m.store.IncRescanCount()
	if m.onSave != nil {
		m.onSave()
	}

	go m.run(job, targets)
	return id, nil
}

func (m *ScanManager) Get(id string) *ScanJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *ScanManager) run(job *ScanJob, targets []Target) {
	msg := fmt.Sprintf("Starting rescan: %d target(s)", len(targets))
	job.addLog(msg)
	log.Printf("rescan: %s", msg)
	var updated int
	for _, t := range targets {
		log.Printf("rescan: %s:%s/%s start", t.Host, t.Port, t.Proto)
		job.addLog(fmt.Sprintf("Target %s:%s/%s", t.Host, t.Port, t.Proto))
		before, _ := m.store.Get(t.Host, t.Port, t.Proto)
		items, err := runNmapRescanStream(t.Host, t.Port, t.Proto, job.addLog)
		if err != nil {
			job.addLog("ERROR: " + err.Error())
			log.Printf("rescan: %s:%s/%s error: %v", t.Host, t.Port, t.Proto, err)
			continue
		}
		if len(items) == 0 {
			m.store.Touch(t.Host, t.Port, t.Proto, "rescan")
			updated++
			after, _ := m.store.Get(t.Host, t.Port, t.Proto)
			job.diff = append(job.diff, Diff{Host: t.Host, Port: t.Port, Proto: t.Proto, Before: before, After: after})
			continue
		}
		for _, it := range items {
			it.Source = "rescan"
			it.LastUpdated = time.Now()
			m.store.Upsert(it, true)
			updated++
			after, _ := m.store.Get(t.Host, t.Port, t.Proto)
			job.diff = append(job.diff, Diff{Host: t.Host, Port: t.Port, Proto: t.Proto, Before: before, After: after})
		}
		log.Printf("rescan: %s:%s/%s done", t.Host, t.Port, t.Proto)
	}
	job.addLog("Done.")
	job.setDone(fmt.Sprintf("Updated records: %d", updated))
	log.Printf("rescan: done (updated=%d)", updated)
	if m.onSave != nil {
		m.onSave()
	}
}

type NmapRun struct {
	Hosts []Host `xml:"host"`
}

type Host struct {
	Addresses []Address `xml:"address"`
	Hostnames Hostnames `xml:"hostnames"`
	Ports     Ports     `xml:"ports"`
}

type Address struct {
	Addr     string `xml:"addr,attr"`
	AddrType string `xml:"addrtype,attr"`
}

type Hostnames struct {
	Names []Hostname `xml:"hostname"`
}

type Hostname struct {
	Name string `xml:"name,attr"`
}

type Ports struct {
	Port []Port `xml:"port"`
}

type Port struct {
	Protocol string  `xml:"protocol,attr"`
	PortId   string  `xml:"portid,attr"`
	State    State   `xml:"state"`
	Service  Service `xml:"service"`
}

type State struct {
	State string `xml:"state,attr"`
}

type Service struct {
	Name      string `xml:"name,attr"`
	Product   string `xml:"product,attr"`
	Version   string `xml:"version,attr"`
	ExtraInfo string `xml:"extrainfo,attr"`
}

func ParseNmapXML(r io.Reader) ([]PortInfo, error) {
	// Import UDP: only open. Import TCP: open + filtered.
	return ParseNmapXMLWithOptions(r, false, false)
}

func ParseNmapXMLWithOptions(r io.Reader, includeOpenFiltered bool, includeAll bool) ([]PortInfo, error) {
	var run NmapRun
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&run); err != nil {
		return nil, err
	}

	var out []PortInfo
	now := time.Now()
	for _, h := range run.Hosts {
		hostIP := pickIP(h.Addresses)
		if hostIP == "" {
			continue
		}
		for _, p := range h.Ports.Port {
			state := strings.ToLower(p.State.State)
			if !includeAll {
				proto := strings.ToLower(strings.TrimSpace(p.Protocol))
				switch proto {
				case "udp":
					if state != "open" {
						continue
					}
				case "tcp":
					if state != "open" && state != "filtered" {
						if includeOpenFiltered && state == "open|filtered" {
							// allow if explicitly requested
						} else {
							continue
						}
					}
				default:
					if state != "open" {
						continue
					}
				}
			}
			if state == "" {
				continue
			}
			if p.PortId == "" || p.Protocol == "" {
				continue
			}
			svc := p.Service.Name
			if svc == "" {
				svc = "unknown"
			}
			out = append(out, PortInfo{
				Host:        hostIP,
				Port:        p.PortId,
				Proto:       p.Protocol,
				State:       normalizeState(state),
				Service:     svc,
				Product:     strings.TrimSpace(p.Service.Product),
				Version:     strings.TrimSpace(p.Service.Version),
				ExtraInfo:   strings.TrimSpace(p.Service.ExtraInfo),
				LastUpdated: now,
				Source:      "upload",
			})
		}
	}
	return out, nil
}

func pickIP(addrs []Address) string {
	for _, a := range addrs {
		if a.AddrType == "ipv4" && a.Addr != "" {
			return a.Addr
		}
	}
	if len(addrs) > 0 {
		return addrs[0].Addr
	}
	return ""
}

func normalizeState(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	return s
}

func stateRank(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "unknown" {
		return 0
	}
	if strings.Contains(s, "|") {
		return 1
	}
	switch s {
	case "filtered", "unfiltered":
		return 2
	case "open", "closed":
		return 3
	default:
		return 1
	}
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Nmap XML Viewer</title>
<style>
:root {
  --bg: #f4f1ed;
  --ink: #1e1b18;
  --muted: #6b5f55;
  --accent: #2c5f2d;
  --accent-2: #ffb400;
  --card: #ffffff;
  --line: #ded6ce;
  --mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
  --sans: "IBM Plex Sans", "Noto Sans", "Segoe UI", system-ui, sans-serif;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  font-family: var(--sans);
  color: var(--ink);
  background:
    radial-gradient(1200px 600px at 10% -20%, #ffe9c7 0%, transparent 60%),
    radial-gradient(1000px 800px at 110% 10%, #d8ead2 0%, transparent 60%),
    var(--bg);
}
header {
  padding: 24px 24px 12px;
}
header h1 { margin: 0 0 6px; font-size: 26px; letter-spacing: 0.4px; }
header p { margin: 0; color: var(--muted); }
main { padding: 16px 24px 48px; max-width: 1200px; }
.card {
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 14px;
  padding: 16px;
  box-shadow: 0 10px 24px rgba(0,0,0,0.06);
}
.row { display: grid; gap: 16px; align-items: end; grid-template-columns: 2fr 1fr 1fr auto; }
.row > * { min-width: 0; }
label { font-weight: 600; }
input[type=file] { width: 100%; }
button {
  background: var(--accent);
  color: #fff;
  border: 0;
  padding: 10px 14px;
  border-radius: 10px;
  font-weight: 600;
  cursor: pointer;
}
button.secondary { background: #3a3a3a; }
button:disabled { opacity: 0.6; cursor: not-allowed; }
select {
  padding: 8px 10px;
  border-radius: 8px;
  border: 1px solid var(--line);
  background: #fff;
}
#status { color: var(--muted); font-size: 14px; }
.group { margin-top: 18px; }
.group h3 {
  margin: 0 0 8px;
  padding: 8px 12px;
  background: #f7f1ea;
  border: 1px solid var(--line);
  border-radius: 10px;
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-family: var(--mono);
  font-size: 13px;
  table-layout: fixed;
}
.table th, .table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--line);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table th { text-align: left; color: var(--muted); }
.badge { background: #efe6da; padding: 2px 6px; border-radius: 6px; font-size: 12px; }
.controls { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; justify-content: flex-end; }
.checkbox { width: 16px; height: 16px; }
.table tr:hover { background: #f9f4ee; }
.row-mark-none {}
.row-mark-green { background: #e7f4ea; }
.row-mark-yellow { background: #fff3cd; }
.row-mark-red { background: #fde2e1; }
.row-mark-blue { background: #e7f0ff; }
.row-mark-gray { background: #f2f2f2; }
.mark-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
  border: 1px solid rgba(0,0,0,0.2);
}
.mark-dot.none { background: transparent; border-color: #c0c0c0; }
.mark-dot.green { background: #2c5f2d; border-color: #2c5f2d; }
.mark-dot.yellow { background: #ffb400; border-color: #ffb400; }
.mark-dot.red { background: #c0392b; border-color: #c0392b; }
.mark-dot.blue { background: #2d5bc6; border-color: #2d5bc6; }
.mark-dot.gray { background: #7a7a7a; border-color: #7a7a7a; }
.note-tag {
  display: inline-block;
  margin-left: 6px;
  font-size: 10px;
  color: #6b5f55;
  background: #efe6da;
  border: 1px solid var(--line);
  border-radius: 6px;
  padding: 1px 4px;
}
.copy-btn {
  background: #dfe9da;
  color: #1f3b21;
  border-radius: 6px;
  padding: 2px 6px;
  font-size: 11px;
  border: 1px solid #c8d7c1;
  cursor: pointer;
}
.group-actions { display: flex; gap: 10px; align-items: center; }
.group-actions label { font-weight: 500; color: var(--muted); }
.group-actions .spacer { flex: 1; }
.filelist { font-size: 12px; color: var(--muted); margin-top: 6px; }
.modal {
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,0.35);
  display: none;
  align-items: center;
  justify-content: center;
  padding: 16px;
}
.modal.open { display: flex; }
.modal-card {
  background: #fff;
  border-radius: 12px;
  padding: 16px;
  width: 100%;
  max-width: 520px;
  border: 1px solid var(--line);
  box-shadow: 0 18px 40px rgba(0,0,0,0.2);
  position: relative;
}
.modal-card h4 { margin: 0 0 8px; }
.modal-close {
  position: absolute;
  top: 8px;
  right: 10px;
  background: transparent;
  border: 0;
  color: #777;
  font-size: 20px;
  cursor: pointer;
}
.kv { display: grid; grid-template-columns: 120px 1fr; gap: 6px 12px; font-family: var(--mono); font-size: 13px; }
.modal-actions { display: flex; gap: 10px; margin-top: 12px; flex-wrap: wrap; }
.modal-actions {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 10px;
}
.toast-wrap {
  position: fixed;
  top: 16px;
  right: 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  z-index: 9999;
}
.toast {
  background: #1b1b1b;
  color: #f6f6f6;
  border-radius: 12px;
  padding: 10px 12px;
  min-width: 240px;
  max-width: 360px;
  box-shadow: 0 12px 26px rgba(0,0,0,0.25);
  display: flex;
  gap: 10px;
  align-items: center;
  cursor: pointer;
}
.toast .meta { font-size: 12px; color: #bdbdbd; }
.toast .title { font-weight: 600; }
.toast .close {
  margin-left: auto;
  background: transparent;
  color: #bdbdbd;
  border: 0;
  font-size: 16px;
  cursor: pointer;
}
.logbox {
  background: #111;
  color: #e8e8e8;
  font-family: var(--mono);
  font-size: 12px;
  padding: 8px;
  border-radius: 8px;
  height: 180px;
  overflow: auto;
  margin-top: 10px;
  white-space: pre-wrap;
}
.diffbox {
  margin-top: 10px;
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 8px;
  font-family: var(--mono);
  font-size: 12px;
}
@media (max-width: 720px) {
  header, main { padding: 16px; }
  .row { grid-template-columns: 1fr; }
  .controls { justify-content: flex-start; }
}
</style>
</head>
<body>
<header>
  <h1>Nmap XML Viewer</h1>
  <p>Upload XML, group by host/port/service, rescan selected ports.</p>
</header>
<main>
  <div class="card">
    <div class="row">
      <div>
        <label>XML files</label>
        <input id="files" type="file" multiple accept=".xml" />
        <div id="filelist" class="filelist"></div>
      </div>
      <div>
        <label>Group by</label>
        <select id="group">
          <option value="service" selected>Service</option>
          <option value="host">Host</option>
          <option value="port">Port</option>
        </select>
      </div>
      <div>
        <label>State</label>
        <select id="stateFilter">
          <option value="all" selected>All</option>
          <option value="open">open</option>
          <option value="filtered">filtered</option>
          <option value="open|filtered">open|filtered</option>
          <option value="closed">closed</option>
          <option value="unknown">unknown</option>
        </select>
      </div>
      <div class="controls">
        <button id="uploadBtn">Upload</button>
        <button id="rescanBtn" class="secondary" disabled>Rescan Selected</button>
        <button id="copyAllBtn" class="secondary">Copy All Hosts</button>
        <button id="clearBtn" class="secondary">Clear Scans</button>
      </div>
    </div>
    <div id="status"></div>
  </div>

  <div id="results"></div>
</main>
<div class="toast-wrap" id="toastWrap"></div>
<div class="modal" id="modal">
  <div class="modal-card">
    <button class="modal-close" id="modalX" aria-label="Close">×</button>
    <h4>Details</h4>
    <div class="kv" id="modalBody"></div>
    <div class="diffbox" id="diffBox" style="display:none"></div>
    <div class="logbox" id="logBox" style="display:none"></div>
    <div style="margin-top:10px">
      <label>Mark</label>
      <select id="markColor">
        <option value="">None</option>
        <option value="green">Green</option>
        <option value="yellow">Yellow</option>
        <option value="red">Red</option>
        <option value="blue">Blue</option>
        <option value="gray">Gray</option>
      </select>
    </div>
    <div style="margin-top:10px">
      <label>Comment</label>
      <textarea id="markComment" rows="3" style="width:100%;padding:8px;border-radius:8px;border:1px solid var(--line);"></textarea>
    </div>
    <div class="modal-actions">
      <button id="modalRescan">Rescan This Port</button>
      <button id="modalCopy" class="secondary">Copy host:port</button>
      <button id="modalDelete" class="secondary">Delete Host</button>
    </div>
  </div>
</div>
<script>
const statusEl = document.getElementById('status');
const resultsEl = document.getElementById('results');
const groupEl = document.getElementById('group');
const stateFilterEl = document.getElementById('stateFilter');
const rescanBtn = document.getElementById('rescanBtn');
const uploadBtn = document.getElementById('uploadBtn');
const copyAllBtn = document.getElementById('copyAllBtn');
const clearBtn = document.getElementById('clearBtn');
const filesEl = document.getElementById('files');
const modal = document.getElementById('modal');
const modalBody = document.getElementById('modalBody');
const modalRescan = document.getElementById('modalRescan');
const modalDelete = document.getElementById('modalDelete');
const modalX = document.getElementById('modalX');
const modalCopy = document.getElementById('modalCopy');
const logBox = document.getElementById('logBox');
const diffBox = document.getElementById('diffBox');
const filelist = document.getElementById('filelist');
const markColor = document.getElementById('markColor');
const markComment = document.getElementById('markComment');
const toastWrap = document.getElementById('toastWrap');

let items = [];
let selected = new Set();
let modalItem = null;
let activeJob = null;
let queuedFiles = [];
let jobs = [];
let jobsSaveTimer = null;
let noteSaveTimer = null;

function keyOf(it){ return it.host + '|' + it.port + '|' + it.proto; }

function infoText(it){
  const parts = [it.product, it.version, it.extrainfo].filter(Boolean);
  return parts.length ? parts.join(' ') : '-';
}

function portSort(a, b){
  const na = parseInt(a.port, 10);
  const nb = parseInt(b.port, 10);
  if (!Number.isNaN(na) && !Number.isNaN(nb) && na !== nb) return na - nb;
  if (a.port !== b.port) return String(a.port).localeCompare(String(b.port));
  return String(a.proto || '').localeCompare(String(b.proto || ''));
}

function sanitizeCopyValue(v){
  return String(v ?? '').replace(/[\t\r\n]+/g, ' ').trim();
}

function formatServicesTable(list){
  const cols = [
    {title: 'Port', get: it => it.port},
    {title: 'Proto', get: it => it.proto},
    {title: 'State', get: it => it.state || 'unknown'},
    {title: 'Service', get: it => it.service || 'unknown'},
    {title: 'Info', get: it => infoText(it)},
  ];
  const rows = list.map(it => cols.map(c => sanitizeCopyValue(c.get(it))));
  const widths = cols.map((c, i) =>
    Math.max(c.title.length, ...rows.map(r => r[i].length))
  );
  const header = cols.map((c, i) => c.title.padEnd(widths[i])).join('  ');
  const lines = rows.map(r => r.map((v, i) => v.padEnd(widths[i])).join('  '));
  return [header, ...lines].join('\n');
}

function render(){
  const group = groupEl.value;
  const stateFilter = stateFilterEl.value;
  const groups = new Map();

  const viewItems = stateFilter === 'all'
    ? items
    : items.filter(it => (it.state || 'unknown').toLowerCase() === stateFilter);

  for (const it of viewItems){
    let g;
    if (group === 'service') g = it.service || 'unknown';
    else if (group === 'host') g = it.host;
    else g = it.port + '/' + it.proto;
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(it);
  }

  const keys = Array.from(groups.keys()).sort();
  let html = '';
  for (const k of keys){
    const list = groups.get(k);
    html += '<div class="group"><h3><div class="group-actions">';
    const allSelected = list.length > 0 && list.every(it => selected.has(keyOf(it)));
    html += '<label><input type="checkbox" class="group-check" data-group="' + k + '"' + (allSelected ? ' checked' : '') + '> Select group</label>';
    html += '<span>' + k + ' <span class="badge">' + list.length + '</span></span>';
    html += '<span class="spacer"></span>';
    html += '<button class="copy-btn group-copy" data-group="' + k + '">Copy all ip:port</button>';
    if (group === 'host'){
      html += '<button class="copy-btn group-copy-services" data-group="' + k + '">Copy services</button>';
    }
    html += '</div></h3>';
    html += '<table class="table"><colgroup>';
    html += '<col style="width:32px">';
    html += '<col style="width:200px">';
    html += '<col style="width:70px">';
    html += '<col style="width:70px">';
    html += '<col style="width:90px">';
    html += '<col style="width:130px">';
    html += '<col style="width:auto">';
    html += '<col style="width:160px">';
    html += '<col style="width:90px">';
    html += '</colgroup><thead><tr>';
    html += '<th></th><th>Host</th><th>Port</th><th>Proto</th><th>State</th><th>Service</th><th>Info</th><th>Updated</th><th>Source</th>';
    html += '</tr></thead><tbody>';
    for (const it of list){
      const k2 = keyOf(it);
      const checked = selected.has(k2) ? 'checked' : '';
    const markClass = 'row-mark-' + (it.color || 'none');
    const markDot = '<span class="mark-dot ' + (it.color || 'none') + '"></span>';
    const noteTag = it.comment ? '<span class="note-tag">note</span>' : '';
    html += '<tr data-key="' + k2 + '" class="' + markClass + '">';
    html += '<td><input class="checkbox" type="checkbox" data-key="' + k2 + '" ' + checked + '></td>';
    html += '<td>' + markDot + '<span class="copy-btn row-copy" data-copy="' + it.host + ':' + it.port + '">copy</span> ' + it.host + noteTag + '</td>';
      html += '<td>' + it.port + '</td>';
      html += '<td>' + it.proto + '</td>';
      html += '<td>' + (it.state || 'unknown') + '</td>';
      html += '<td>' + it.service + '</td>';
      html += '<td>' + infoText(it) + '</td>';
      html += '<td>' + new Date(it.lastUpdated).toLocaleString() + '</td>';
      html += '<td>' + it.source + '</td>';
      html += '</tr>';
    }
    html += '</tbody></table></div>';
  }
  resultsEl.innerHTML = html;
  for (const cb of resultsEl.querySelectorAll('input[type=checkbox]')){
    cb.addEventListener('change', (e) => {
      const key = e.target.getAttribute('data-key');
      if (e.target.checked) selected.add(key); else selected.delete(key);
      rescanBtn.disabled = selected.size === 0;
    });
    cb.addEventListener('click', (e) => e.stopPropagation());
  }
  for (const gb of resultsEl.querySelectorAll('.group-check')){
    gb.addEventListener('change', (e) => {
      const g = e.target.getAttribute('data-group');
      const list = groups.get(g) || [];
      if (e.target.checked){
        for (const it of list) selected.add(keyOf(it));
      } else {
        for (const it of list) selected.delete(keyOf(it));
      }
      render();
    });
  }
  for (const gc of resultsEl.querySelectorAll('.group-copy')){
    gc.addEventListener('click', async (e) => {
      e.stopPropagation();
      const g = gc.getAttribute('data-group');
      const list = groups.get(g) || [];
      const lines = Array.from(new Set(list.map(it => it.host + ':' + it.port)));
      const text = lines.join('\n');
      try { await navigator.clipboard.writeText(text); statusEl.textContent = 'Copied group: ' + g; }
      catch { statusEl.textContent = 'Copy failed'; }
    });
  }
  for (const gs of resultsEl.querySelectorAll('.group-copy-services')){
    gs.addEventListener('click', async (e) => {
      e.stopPropagation();
      const g = gs.getAttribute('data-group');
      const list = (groups.get(g) || []).slice().sort(portSort);
      const text = formatServicesTable(list);
      try { await navigator.clipboard.writeText(text); statusEl.textContent = 'Copied services: ' + g; }
      catch { statusEl.textContent = 'Copy failed'; }
    });
  }
  for (const copyBtn of resultsEl.querySelectorAll('.row-copy')){
    copyBtn.addEventListener('click', async (e) => {
      e.stopPropagation();
      const text = copyBtn.getAttribute('data-copy');
      try { await navigator.clipboard.writeText(text); statusEl.textContent = 'Copied: ' + text; }
      catch { statusEl.textContent = 'Copy failed'; }
    });
  }
  for (const tr of resultsEl.querySelectorAll('tr[data-key]')){
    tr.addEventListener('click', () => {
      const key = tr.getAttribute('data-key');
      modalItem = items.find(it => keyOf(it) === key);
      if (!modalItem) return;
      const rows = [
        ['Host', modalItem.host],
        ['Port', modalItem.port],
        ['Proto', modalItem.proto],
        ['State', modalItem.state || 'unknown'],
        ['Service', modalItem.service],
        ['Product', modalItem.product || '-'],
        ['Version', modalItem.version || '-'],
        ['Extra', modalItem.extrainfo || '-'],
        ['Updated', new Date(modalItem.lastUpdated).toLocaleString()],
        ['Source', modalItem.source],
      ];
      modalBody.innerHTML = rows.map(r => '<div>' + r[0] + '</div><div>' + r[1] + '</div>').join('');
      markColor.value = modalItem.color || '';
      markComment.value = modalItem.comment || '';
      showPortLogs(modalItem.host, modalItem.port, modalItem.proto);
      diffBox.style.display = 'none';
      modal.classList.add('open');
    });
  }
  rescanBtn.disabled = selected.size === 0;
}

function collectPortLogs(host, port, proto){
  const out = [];
  const h = String(host || '');
  const p = String(port || '');
  const pr = String(proto || '').toLowerCase();
  for (const j of jobs){
    const targets = Array.isArray(j.targetList) ? j.targetList : [];
    const match = targets.some(t =>
      String(t.host || '') === h &&
      String(t.port || '') === p &&
      String(t.proto || '').toLowerCase() === pr
    );
    if (!match) continue;
    if (!j.logs || !j.logs.length) continue;
    out.push('--- ' + j.id + ' ---');
    for (const line of j.logs){
      out.push(line);
    }
  }
  return out;
}

function showPortLogs(host, port, proto){
  const lines = collectPortLogs(host, port, proto).slice(-400);
  if (!lines.length){
    logBox.style.display = 'none';
    return;
  }
  logBox.textContent = lines.join('\n');
  logBox.style.display = 'block';
}

async function load(){
  const res = await fetch('/api/items');
  items = await res.json();
  render();
}

uploadBtn.addEventListener('click', async () => {
  const files = queuedFiles.length ? queuedFiles : Array.from(filesEl.files || []);
  if (!files || files.length === 0){
    statusEl.textContent = 'Select at least one XML file.';
    return;
  }
  const fd = new FormData();
  for (const f of files) fd.append('files', f, f.name);
  statusEl.textContent = 'Uploading...';
  const res = await fetch('/api/upload', { method: 'POST', body: fd });
  const data = await res.json();
  statusEl.textContent = data.message || 'Done.';
  queuedFiles = [];
  filelist.textContent = '';
  await load();
});

rescanBtn.addEventListener('click', async () => {
  const targets = [];
  for (const it of items){
    const k = keyOf(it);
    if (selected.has(k)) targets.push({host: it.host, port: it.port, proto: it.proto});
  }
  if (targets.length === 0) return;
  statusEl.textContent = 'Rescan ' + targets.length + '...';
  rescanBtn.disabled = true;
  const res = await fetch('/api/rescan', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({targets})
  });
  const data = await res.json();
  statusEl.textContent = data.message || 'Started.';
  if (data.jobId){
    const job = addJob({
      id: data.jobId,
      message: 'Rescan started',
      targets: targets.length,
      targetList: targets,
      status: 'running',
      createdAt: new Date(),
      logs: [],
      diffs: [],
    });
    startJobLogStream(job);
    await waitForJob(data.jobId, job);
  }
  await load();
});

copyAllBtn.addEventListener('click', async () => {
  if (!items.length){
    statusEl.textContent = 'No items to copy.';
    return;
  }
  const lines = Array.from(new Set(items.map(it => it.host + ':' + it.port)));
  const text = lines.join('\n');
  try { await navigator.clipboard.writeText(text); statusEl.textContent = 'Copied all hosts: ' + lines.length; }
  catch { statusEl.textContent = 'Copy failed'; }
});

clearBtn.addEventListener('click', async () => {
  if (!items.length){
    statusEl.textContent = 'No scans to clear.';
    return;
  }
  if (!confirm('Clear all scans? This will remove all loaded records.')) return;
  const res = await fetch('/api/clear', { method: 'POST' });
  const data = await res.json();
  statusEl.textContent = data.message || 'Cleared.';
  items = [];
  selected.clear();
  render();
});

modalX.addEventListener('click', () => {
  modal.classList.remove('open');
});
modal.addEventListener('click', (e) => {
  if (e.target === modal) modal.classList.remove('open');
});
modalRescan.addEventListener('click', async () => {
  if (!modalItem) return;
  statusEl.textContent = 'Rescan 1...';
  logBox.textContent = '';
  logBox.style.display = 'block';
  diffBox.style.display = 'none';
  const res = await fetch('/api/rescan', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({targets: [{host: modalItem.host, port: modalItem.port, proto: modalItem.proto}]})
  });
  const data = await res.json();
  if (data.jobId){
    const job = addJob({
      id: data.jobId,
      message: 'Rescan started',
      targets: 1,
      targetList: [{host: modalItem.host, port: modalItem.port, proto: modalItem.proto}],
      status: 'running',
      createdAt: new Date(),
      logs: [],
      diffs: [],
    });
    startJobLogStream(job);
    await streamJobLogs(data.jobId);
    const result = await fetch('/api/rescan/result?job=' + encodeURIComponent(data.jobId));
    const rdata = await result.json();
    showDiff(rdata.diffs || []);
    showPortLogs(modalItem.host, modalItem.port, modalItem.proto);
    statusEl.textContent = rdata.message || 'Done.';
  } else {
    statusEl.textContent = data.message || 'Done.';
  }
  await load();
});
modalCopy.addEventListener('click', async () => {
  if (!modalItem) return;
  const text = modalItem.host + ':' + modalItem.port;
  try { await navigator.clipboard.writeText(text); statusEl.textContent = 'Copied: ' + text; }
  catch { statusEl.textContent = 'Copy failed'; }
});
function scheduleNoteSave(){
  if (!modalItem) return;
  if (noteSaveTimer) clearTimeout(noteSaveTimer);
  noteSaveTimer = setTimeout(async () => {
    if (!modalItem) return;
    const nextColor = markColor.value || '';
    const nextComment = markComment.value || '';
    if (nextColor === (modalItem.color || '') && nextComment === (modalItem.comment || '')) return;
    const res = await fetch('/api/annotate', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        host: modalItem.host,
        port: modalItem.port,
        proto: modalItem.proto,
        color: nextColor,
        comment: nextComment,
      })
    });
    const data = await res.json();
    if (data && data.item){
      const k = keyOf(data.item);
      const idx = items.findIndex(it => keyOf(it) === k);
      if (idx >= 0) items[idx] = data.item;
      modalItem = data.item;
      statusEl.textContent = 'Saved note.';
      render();
    } else {
      statusEl.textContent = data.message || 'Save failed.';
    }
  }, 400);
}

markColor.addEventListener('change', scheduleNoteSave);
markComment.addEventListener('input', scheduleNoteSave);
modalDelete.addEventListener('click', async () => {
  if (!modalItem) return;
  if (!confirm('Delete all records for host ' + modalItem.host + '?')) return;
  const res = await fetch('/api/host/delete', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({host: modalItem.host})
  });
  const data = await res.json();
  statusEl.textContent = data.message || 'Done.';
  modal.classList.remove('open');
  await load();
});

groupEl.addEventListener('change', render);
stateFilterEl.addEventListener('change', render);

load();
loadJobsFromStorage();

async function waitForJob(jobId, job){
  while (true){
    const res = await fetch('/api/rescan/status?job=' + encodeURIComponent(jobId));
    const data = await res.json();
    if (data.done){
      if (job){
        job.status = 'done';
        job.completedAt = new Date();
        const result = await fetch('/api/rescan/result?job=' + encodeURIComponent(jobId));
        const rdata = await result.json();
        job.diffs = rdata.diffs || [];
        if (Array.isArray(rdata.logs) && rdata.logs.length){
          job.logs = rdata.logs.slice(-2000);
        }
        job.message = rdata.message || 'Done';
        updateToast(jobId, 'Rescan complete', true);
        scheduleJobsSave();
      }
      return;
    }
    await new Promise(r => setTimeout(r, 1000));
  }
}

function addJob(job){
  jobs.unshift(job);
  showToast(job);
  scheduleJobsSave();
  return job;
}

function showToast(job){
  const el = document.createElement('div');
  el.className = 'toast';
  el.setAttribute('data-job', job.id);
  el.innerHTML =
    '<div>' +
    '<div class="title">Rescan started</div>' +
    '<div class="meta">' + job.targets + ' target(s)</div>' +
    '</div>' +
    '<button class="close" aria-label="Dismiss">×</button>';
  el.addEventListener('click', (e) => {
    if (e.target && e.target.classList.contains('close')) return;
  });
  let startX = null;
  el.addEventListener('pointerdown', (e) => {
    startX = e.clientX;
  });
  el.addEventListener('pointermove', (e) => {
    if (startX === null) return;
    if (e.clientX - startX > 80) {
      startX = null;
      el.remove();
    }
  });
  el.addEventListener('pointerup', () => { startX = null; });
  el.addEventListener('pointercancel', () => { startX = null; });
  el.querySelector('.close').addEventListener('click', (e) => {
    e.stopPropagation();
    el.remove();
  });
  toastWrap.prepend(el);
  setTimeout(() => { if (el.isConnected) el.remove(); }, 8000);
}

function updateToast(jobId, title, sticky){
  const el = toastWrap.querySelector('.toast[data-job="' + jobId + '"]');
  if (!el) return;
  const titleEl = el.querySelector('.title');
  if (titleEl) titleEl.textContent = title;
  if (sticky) return;
  setTimeout(() => { if (el.isConnected) el.remove(); }, 4000);
}

function startJobLogStream(job){
  const es = new EventSource('/api/rescan/stream?job=' + encodeURIComponent(job.id));
  es.addEventListener('log', (e) => {
    job.logs.push(e.data);
    if (job.logs.length > 2000) job.logs.shift();
    scheduleJobsSave();
  });
  es.addEventListener('done', () => es.close());
  es.addEventListener('error', () => es.close());
}

function scheduleJobsSave(){
  if (jobsSaveTimer) return;
  jobsSaveTimer = setTimeout(() => {
    jobsSaveTimer = null;
    saveJobsToStorage();
  }, 300);
}

function saveJobsToStorage(){
  const slim = jobs.map(j => ({
    id: j.id,
    message: j.message,
    targets: j.targets,
    targetList: Array.isArray(j.targetList) ? j.targetList : [],
    status: j.status,
    createdAt: j.createdAt ? j.createdAt.toISOString() : '',
    completedAt: j.completedAt ? j.completedAt.toISOString() : '',
    diffs: j.diffs || [],
    logs: (j.logs || []).slice(-2000),
  }));
  try { localStorage.setItem('rescan_jobs', JSON.stringify(slim)); } catch {}
}

function loadJobsFromStorage(){
  try {
    const raw = localStorage.getItem('rescan_jobs');
    if (!raw) return;
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return;
    jobs = arr.map(j => ({
      id: j.id,
      message: j.message,
      targets: j.targets,
      targetList: Array.isArray(j.targetList) ? j.targetList : [],
      status: j.status,
      createdAt: j.createdAt ? new Date(j.createdAt) : null,
      completedAt: j.completedAt ? new Date(j.completedAt) : null,
      diffs: j.diffs || [],
      logs: Array.isArray(j.logs) ? j.logs : [],
    }));
  } catch {}
}

async function streamJobLogs(jobId){
  if (activeJob) activeJob.close();
  return new Promise((resolve) => {
    const es = new EventSource('/api/rescan/stream?job=' + encodeURIComponent(jobId));
    activeJob = es;
    es.addEventListener('log', (e) => {
      logBox.textContent += e.data + '\n';
      logBox.scrollTop = logBox.scrollHeight;
    });
    es.addEventListener('done', () => {
      es.close();
      resolve();
    });
    es.addEventListener('error', () => {
      es.close();
      resolve();
    });
  });
}

function showDiff(diffs){
  if (!diffs || diffs.length === 0){
    diffBox.style.display = 'none';
    return;
  }
  const d = diffs[0];
  const lines = [];
  lines.push('Before: state=' + (d.before.state || '-') + ' service=' + (d.before.service || '-') + ' product=' + (d.before.product || '-') + ' version=' + (d.before.version || '-') + ' extra=' + (d.before.extrainfo || '-'));
  lines.push('After:  state=' + (d.after.state || '-') + ' service=' + (d.after.service || '-') + ' product=' + (d.after.product || '-') + ' version=' + (d.after.version || '-') + ' extra=' + (d.after.extrainfo || '-'));
  diffBox.textContent = lines.join('\n');
  diffBox.style.display = 'block';
}

function updateFileList(){
  if (!queuedFiles.length){
    filelist.textContent = '';
    return;
  }
  filelist.textContent = 'Queued: ' + queuedFiles.map(f => f.name).join(', ');
}

filesEl.addEventListener('change', () => {
  queuedFiles = Array.from(filesEl.files || []);
  updateFileList();
});
</script>
</body>
</html>`))

func main() {
	dbFlag := flag.String("db", "", "Path to DB file (JSON)")
	addrFlag := flag.String("addr", ":8080", "Listen address")
	flag.Parse()

	dbPath := resolveDBPath(*dbFlag)
	store := NewStore()
	if dbPath != "" {
		if err := store.Load(dbPath); err == nil {
			log.Printf("db: loaded %s", dbPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("db: load error: %v", err)
		}
	}
	saveDB := func() {
		if dbPath == "" {
			return
		}
		if err := store.Save(dbPath); err != nil {
			log.Printf("db: save error: %v", err)
		}
	}
	scans := NewScanManager(store, saveDB)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pageTmpl.Execute(w, nil)
	})

	http.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.List())
	})

	http.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		files := r.MultipartForm.File["files"]
		if len(files) == 0 {
			http.Error(w, "no files", http.StatusBadRequest)
			return
		}

		var total int
		log.Printf("upload: %d file(s)", len(files))
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			items, err := ParseNmapXML(f)
			_ = f.Close()
			if err != nil {
				continue
			}
			for _, it := range items {
				store.Upsert(it, false)
				total++
			}
		}
		log.Printf("upload: imported %d record(s)", total)
		saveDB()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": fmt.Sprintf("Imported records: %d", total),
		})
	})

	http.HandleFunc("/api/rescan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Targets []Target `json:"targets"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Targets) == 0 {
			http.Error(w, "no targets", http.StatusBadRequest)
			return
		}

		if _, err := exec.LookPath("nmap"); err != nil {
			http.Error(w, "nmap not found in PATH", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		jobID, err := scans.Start(req.Targets)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Rescan started",
			"jobId":   jobID,
		})
	})

	http.HandleFunc("/api/rescan/stream", func(w http.ResponseWriter, r *http.Request) {
		jobID := r.URL.Query().Get("job")
		if jobID == "" {
			http.Error(w, "job required", http.StatusBadRequest)
			return
		}
		job := scans.Get(jobID)
		if job == nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream not supported", http.StatusInternalServerError)
			return
		}

		ch := job.Subscribe()
		defer job.Unsubscribe(ch)

		for _, line := range job.Logs() {
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", escapeSSE(line))
		}
		flusher.Flush()

		for {
			select {
			case line, ok := <-ch:
				if !ok {
					fmt.Fprint(w, "event: done\ndata: done\n\n")
					flusher.Flush()
					return
				}
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", escapeSSE(line))
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	http.HandleFunc("/api/rescan/status", func(w http.ResponseWriter, r *http.Request) {
		jobID := r.URL.Query().Get("job")
		if jobID == "" {
			http.Error(w, "job required", http.StatusBadRequest)
			return
		}
		job := scans.Get(jobID)
		if job == nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"done": job.Done(),
		})
	})

	http.HandleFunc("/api/rescan/count", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": store.RescanCount(),
		})
	})

	http.HandleFunc("/api/rescan/result", func(w http.ResponseWriter, r *http.Request) {
		jobID := r.URL.Query().Get("job")
		if jobID == "" {
			http.Error(w, "job required", http.StatusBadRequest)
			return
		}
		job := scans.Get(jobID)
		if job == nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"done":    job.Done(),
			"message": job.Message(),
			"diffs":   job.Diffs(),
			"logs":    job.Logs(),
		})
	})

	http.HandleFunc("/api/host/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Host == "" {
			http.Error(w, "host required", http.StatusBadRequest)
			return
		}
		deleted := store.DeleteHost(req.Host)
		log.Printf("delete host: %s (%d record(s))", req.Host, deleted)
		saveDB()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": fmt.Sprintf("Deleted records: %d", deleted),
		})
	})
	http.HandleFunc("/api/annotate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Host    string `json:"host"`
			Port    string `json:"port"`
			Proto   string `json:"proto"`
			Color   string `json:"color"`
			Comment string `json:"comment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		item, ok := store.UpdateAnnotation(req.Host, req.Port, req.Proto, strings.TrimSpace(req.Color), strings.TrimSpace(req.Comment))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		saveDB()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Saved",
			"item":    item,
		})
	})
	http.HandleFunc("/api/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store.Clear()
		saveDB()
		log.Printf("clear: all records removed")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "All scans cleared.",
		})
	})

	log.Printf("listening on %s", *addrFlag)
	log.Fatal(http.ListenAndServe(*addrFlag, nil))
}

func runNmapRescanStream(host, port, proto string, logFn func(string)) ([]PortInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tmp, err := os.CreateTemp("", "nmapxml-*.xml")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	args := []string{"-Pn", "-A", "-p", port, "-oX", tmpPath, "-oN", "-"}
	if strings.ToLower(proto) == "udp" {
		args = append([]string{"-sU"}, args...)
	}
	args = append(args, host)

	cmd := exec.CommandContext(ctx, "nmap", args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		readLines(stdoutPipe, logFn)
	}()
	go func() {
		defer wg.Done()
		readLines(stderrPipe, logFn)
	}()

	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		return nil, fmt.Errorf("nmap failed: %v", err)
	}

	b, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}
	items, err := ParseNmapXMLWithOptions(bytes.NewReader(b), true, true)
	if err != nil {
		return nil, err
	}

	filtered := items[:0]
	for _, it := range items {
		if it.Host == host && it.Port == port && strings.ToLower(it.Proto) == strings.ToLower(proto) {
			filtered = append(filtered, it)
		}
	}
	return filtered, nil
}

func readLines(r io.Reader, logFn func(string)) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			logFn(line)
		}
	}
}

func escapeSSE(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func resolveDBPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	p := filepath.Join(cwd, "nmap_viewer.db.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// Allow dropping a single file via CLI to pre-load data when starting server.
