// Package server serves the single-page web app and its JSON/SSE API.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dmetric.dalang.io/internal/model"
	"dmetric.dalang.io/internal/monitor"
	"dmetric.dalang.io/internal/store"
	"dmetric.dalang.io/internal/update"
)

//go:embed ui
var uiFiles embed.FS

// Server is the web application. The desktop window loads the same page.
type Server struct {
	mon             *monitor.Monitor
	store           *store.Store
	settings        model.Settings
	effectivePort   int
	effectiveSecond int
	host            string
	dataDir         string
	portLocked      bool
	intervalLocked  bool
	updater         *update.Manager
	start           time.Time

	mu         sync.Mutex
	httpServer *http.Server

	shutdownCh   chan struct{}
	shutdownOnce sync.Once
}

// New creates the server. effectivePort and effectiveInterval are the
// values the process was started with; settings may differ until restart.
func New(mon *monitor.Monitor, st *store.Store, settings model.Settings,
	effectivePort, effectiveInterval int, host, dataDir string,
	portLocked, intervalLocked bool) *Server {
	return &Server{
		mon:             mon,
		store:           st,
		settings:        settings,
		effectivePort:   effectivePort,
		effectiveSecond: effectiveInterval,
		host:            host,
		dataDir:         dataDir,
		portLocked:      portLocked,
		intervalLocked:  intervalLocked,
		start:           time.Now(),
		shutdownCh:      make(chan struct{}),
	}
}

// Serve serves HTTP on the listener until Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Lock()
	s.httpServer = srv
	s.mu.Unlock()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpServer
	s.mu.Unlock()
	// End the SSE streams first; they would otherwise keep Shutdown
	// waiting for its deadline.
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
	if srv == nil {
		return nil
	}
	err := srv.Shutdown(ctx)
	if err != nil {
		// Force-close remaining connections, including SSE streams that
		// did not see the channel in time.
		_ = srv.Close()
	}
	return err
}

// Handler returns the HTTP handler, for tests and for Serve.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/alerts", s.handleAlerts)
	mux.HandleFunc("POST /api/alerts/{id}/ack", s.handleAck)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/update/install", s.handleUpdateInstall)
	mux.HandleFunc("GET /api/candidates", s.handleCandidates)

	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServerFS(sub))

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Latest())
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming tidak didukung")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	ch, unsubscribe := s.mon.Subscribe()
	defer unsubscribe()

	send := func(snap model.Snapshot) bool {
		data, err := json.Marshal(snap)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send(s.mon.Latest()) {
		return
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCh:
			return
		case snap := <-ch:
			if !send(snap) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	minutes := 30
	if raw := r.URL.Query().Get("minutes"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 24*60 {
			writeError(w, http.StatusBadRequest, "minutes harus antara 1 dan 1440")
			return
		}
		minutes = n
	}
	points, err := s.store.History(minutes)
	if err != nil {
		log.Printf("dmetric: riwayat: %v", err)
		writeError(w, http.StatusInternalServerError, "gagal membaca riwayat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"minutes": minutes,
		"points":  points,
	})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 500 {
			limit = n
		}
	}
	alerts, err := s.store.ListAlerts(limit)
	if err != nil {
		log.Printf("dmetric: daftar alert: %v", err)
		writeError(w, http.StatusInternalServerError, "gagal membaca alert")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id alert tidak valid")
		return
	}
	if err := s.mon.Ack(id); err != nil {
		log.Printf("dmetric: ack alert: %v", err)
		writeError(w, http.StatusInternalServerError, "gagal menandai alert")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type settingsView struct {
	Settings          model.Settings `json:"settings"`
	EffectivePort     int            `json:"effective_port"`
	EffectiveInterval int            `json:"effective_interval"`
	PortLocked        bool           `json:"port_locked"`
	IntervalLocked    bool           `json:"interval_locked"`
	RestartRequired   bool           `json:"restart_required"`
	Host              string         `json:"host"`
	DataDir           string         `json:"data_dir"`
	Version           string         `json:"version"`
}

func (s *Server) view() settingsView {
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()
	restart := false
	if !s.portLocked && settings.Port != s.effectivePort {
		restart = true
	}
	if !s.intervalLocked && settings.IntervalSeconds != s.effectiveSecond {
		restart = true
	}
	return settingsView{
		Settings:          settings,
		EffectivePort:     s.effectivePort,
		EffectiveInterval: s.effectiveSecond,
		PortLocked:        s.portLocked,
		IntervalLocked:    s.intervalLocked,
		RestartRequired:   restart,
		Host:              s.host,
		DataDir:           s.dataDir,
		Version:           model.Version,
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var incoming model.Settings
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeError(w, http.StatusBadRequest, "JSON pengaturan tidak valid")
		return
	}
	incoming.Normalize()

	s.mu.Lock()
	current := s.settings
	s.mu.Unlock()
	if s.portLocked && incoming.Port != current.Port {
		writeError(w, http.StatusBadRequest, "port ditetapkan oleh flag atau variabel lingkungan")
		return
	}
	if s.intervalLocked && incoming.IntervalSeconds != current.IntervalSeconds {
		writeError(w, http.StatusBadRequest, "interval ditetapkan oleh flag atau variabel lingkungan")
		return
	}
	if err := incoming.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SaveSettings(incoming); err != nil {
		log.Printf("dmetric: menyimpan pengaturan: %v", err)
		writeError(w, http.StatusInternalServerError, "gagal menyimpan pengaturan")
		return
	}
	s.mu.Lock()
	s.settings = incoming
	s.mu.Unlock()
	s.mon.SetThresholds(incoming.Thresholds)
	s.mon.SetWatchApps(incoming.WatchApps)
	if !slices.Equal(current.NetworkProbes, incoming.NetworkProbes) {
		s.mon.SetNetworkProbes(incoming.NetworkProbes)
	}
	if !slices.Equal(current.NetworkInterfaces, incoming.NetworkInterfaces) {
		s.mon.SetNetworkInterfaces(incoming.NetworkInterfaces)
	}
	writeJSON(w, http.StatusOK, s.view())
}

type candidateProcess struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	CPUPercent float64 `json:"cpu_percent"`
	MemoryRSS  uint64  `json:"memory_rss"`
}

type candidateTarget struct {
	Target string `json:"target"`
	Label  string `json:"label"`
}

type candidateInterface struct {
	Name          string  `json:"name"`
	RxBytesTotal  uint64  `json:"rx_bytes_total"`
	TxBytesTotal  uint64  `json:"tx_bytes_total"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

type candidatesView struct {
	Processes          []candidateProcess   `json:"processes"`
	Targets            []candidateTarget    `json:"targets"`
	Interfaces         []candidateInterface `json:"interfaces"`
	SelectedInterfaces []string             `json:"selected_interfaces"`
	Gateway            string               `json:"gateway"`
}

func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	snap := s.mon.Latest()
	byName := map[string]*candidateProcess{}
	for _, p := range snap.AllProcesses {
		key := strings.ToLower(strings.TrimSpace(p.Name))
		if key == "" {
			continue
		}
		item := byName[key]
		if item == nil {
			item = &candidateProcess{Name: strings.TrimSpace(p.Name)}
			byName[key] = item
		}
		item.Count++
		item.CPUPercent += p.CPUPercent
		item.MemoryRSS += p.MemoryRSS
	}
	processes := make([]candidateProcess, 0, len(byName))
	for _, item := range byName {
		processes = append(processes, *item)
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].CPUPercent != processes[j].CPUPercent {
			return processes[i].CPUPercent > processes[j].CPUPercent
		}
		return processes[i].MemoryRSS > processes[j].MemoryRSS
	})
	if len(processes) > 120 {
		processes = processes[:120]
	}

	targets := make([]candidateTarget, 0, 40)
	seenTarget := map[string]bool{}
	for _, port := range snap.Ports.Items {
		if port.Proto != "tcp" || port.Port == 0 {
			continue
		}
		host := strings.TrimSpace(port.Address)
		if host == "" || host == "*" || net.ParseIP(host) == nil {
			host = "127.0.0.1"
		}
		target := net.JoinHostPort(host, strconv.Itoa(int(port.Port)))
		if seenTarget[target] {
			continue
		}
		seenTarget[target] = true
		label := fmt.Sprintf("%s · %s:%d", port.Process, port.Proto, port.Port)
		targets = append(targets, candidateTarget{Target: target, Label: label})
		if len(targets) >= 40 {
			break
		}
	}

	interfaces := make([]candidateInterface, 0, len(snap.Network.Interfaces))
	for _, iface := range snap.Network.Interfaces {
		interfaces = append(interfaces, candidateInterface{
			Name:          iface.Name,
			RxBytesTotal:  iface.RxBytesTotal,
			TxBytesTotal:  iface.TxBytesTotal,
			RxBytesPerSec: iface.RxBytesPerSec,
			TxBytesPerSec: iface.TxBytesPerSec,
		})
	}
	sort.Slice(interfaces, func(i, j int) bool {
		totalI := interfaces[i].RxBytesTotal + interfaces[i].TxBytesTotal
		totalJ := interfaces[j].RxBytesTotal + interfaces[j].TxBytesTotal
		if totalI != totalJ {
			return totalI > totalJ
		}
		return interfaces[i].Name < interfaces[j].Name
	})
	s.mu.Lock()
	selected := append([]string{}, s.settings.NetworkInterfaces...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, candidatesView{
		Processes:          processes,
		Targets:            targets,
		Interfaces:         interfaces,
		SelectedInterfaces: selected,
		Gateway:            snap.Network.Gateway,
	})
}

// SetUpdater enables the GitHub update endpoints.
func (s *Server) SetUpdater(updater *update.Manager) {
	s.mu.Lock()
	s.updater = updater
	s.mu.Unlock()
}

type updateView struct {
	update.Info
	InstallAllowed bool `json:"install_allowed"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	updater := s.updater
	s.mu.Unlock()
	if updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater tidak aktif")
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	info := updater.Check(r.Context(), force)
	writeJSON(w, http.StatusOK, updateView{Info: info, InstallAllowed: requestIsLocal(r)})
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !requestIsLocal(r) {
		writeError(w, http.StatusForbidden, "update harus dipicu dari server (localhost), bukan dari client")
		return
	}
	s.mu.Lock()
	updater := s.updater
	s.mu.Unlock()
	if updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater tidak aktif")
		return
	}
	var request struct {
		Version string `json:"version"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "JSON permintaan tidak valid")
		return
	}
	result, err := updater.Install(r.Context(), request.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"version":     result.Version,
		"release_url": result.ReleaseURL,
		"message":     result.Message,
		"restarting":  result.Restart != nil,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if result.Restart != nil {
		time.AfterFunc(800*time.Millisecond, func() {
			if err := result.Restart(); err != nil {
				log.Printf("dmetric: restart setelah update: %v", err)
			}
		})
	}
}

func requestIsLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"version":        model.Version,
		"uptime_seconds": int64(time.Since(s.start).Seconds()),
		"port":           s.effectivePort,
		"host":           s.host,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("dmetric: menulis respons: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
