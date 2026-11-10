// Command dmetric runs DMetric: a desktop window built with MyGo and the
// same dashboard as a web app on http://127.0.0.1:8154 (configurable).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"

	"dmetric.dalang.io/internal/model"
	"dmetric.dalang.io/internal/monitor"
	"dmetric.dalang.io/internal/server"
	"dmetric.dalang.io/internal/store"
	"dmetric.dalang.io/internal/update"
)

func main() {
	var (
		portFlag     int
		hostFlag     string
		intervalFlag int
		dataFlag     string
		noWindow     bool
		showVersion  bool
		updateRepo   string
	)
	flag.IntVar(&portFlag, "port", 0, "port HTTP (bawaan 8154; 0 memakai pengaturan tersimpan)")
	flag.StringVar(&hostFlag, "host", "", "alamat bind HTTP (bawaan 127.0.0.1)")
	flag.IntVar(&intervalFlag, "interval", 0, "interval sampling dalam detik (bawaan 2)")
	flag.StringVar(&dataFlag, "data", "", "direktori data (bawaan direktori konfigurasi pengguna/DMetric)")
	flag.BoolVar(&noWindow, "no-window", false, "jalankan hanya server web, tanpa jendela desktop")
	flag.StringVar(&updateRepo, "update-repo", "", "repo GitHub owner/name untuk update (bawaan dalang-io/dmetric.dalang.io)")
	flag.BoolVar(&showVersion, "version", false, "tampilkan versi lalu keluar")
	flag.Parse()

	if showVersion {
		fmt.Printf("DMetric %s\n", model.Version)
		return
	}

	dataDir := dataFlag
	if dataDir == "" {
		dataDir = os.Getenv("DMETRIC_DATA")
	}
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		log.Fatalf("direktori data tidak valid: %v", err)
	}

	st, err := store.Open(filepath.Join(dataDir, "dmetric.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.CloseOpenAlerts(model.NowMillis()); err != nil {
		log.Printf("dmetric: menutup alert lama: %v", err)
	}
	if err := st.Prune(time.Now()); err != nil {
		log.Printf("dmetric: membersihkan data lama: %v", err)
	}

	settings := st.LoadSettings()

	effectivePort := settings.Port
	portLocked := false
	if flagSet("port") {
		effectivePort = portFlag
		portLocked = true
	} else if raw := os.Getenv("DMETRIC_PORT"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 65535 {
			effectivePort = value
			portLocked = true
		} else {
			log.Printf("dmetric: DMETRIC_PORT=%q tidak valid, diabaikan", raw)
		}
	}

	effectiveInterval := settings.IntervalSeconds
	intervalLocked := false
	if flagSet("interval") {
		effectiveInterval = intervalFlag
		intervalLocked = true
	} else if raw := os.Getenv("DMETRIC_INTERVAL"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 1 && value <= 60 {
			effectiveInterval = value
			intervalLocked = true
		} else {
			log.Printf("dmetric: DMETRIC_INTERVAL=%q tidak valid, diabaikan", raw)
		}
	}
	if effectivePort < 1 || effectivePort > 65535 {
		log.Fatalf("port %d tidak valid", effectivePort)
	}
	if effectiveInterval < 1 || effectiveInterval > 60 {
		log.Fatalf("interval %d detik tidak valid (1-60)", effectiveInterval)
	}

	bindHost := "127.0.0.1"
	if raw := os.Getenv("DMETRIC_HOST"); raw != "" {
		bindHost = raw
	}
	if hostFlag != "" {
		bindHost = hostFlag
	}

	addr := net.JoinHostPort(bindHost, strconv.Itoa(effectivePort))
	// Bind before the first sample so a probe to this app's own port does
	// not see a refused connection.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("tidak bisa membuka %s: %v\nGunakan --port atau DMETRIC_PORT untuk port lain.", addr, err)
	}

	mon := monitor.New(st, st, time.Duration(effectiveInterval)*time.Second, settings.Thresholds,
		settings.WatchApps, settings.NetworkProbes, settings.NetworkInterfaces)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Run(ctx)

	srv := server.New(mon, st, settings, effectivePort, effectiveInterval,
		bindHost, dataDir, portLocked, intervalLocked)
	repo := updateRepo
	if repo == "" {
		repo = os.Getenv("DMETRIC_UPDATE_REPO")
	}
	updater := update.New(dataDir, model.Version, repo)
	srv.SetUpdater(updater)
	// Background update checks/installs follow Settings -> Pembaruan (manual
	// by default: nothing runs unless the operator turns it on).
	go updater.RunAuto(ctx, srv.UpdatePolicy, func(result update.Result) {
		if result.Restart == nil {
			return
		}
		time.AfterFunc(800*time.Millisecond, func() {
			if err := result.Restart(); err != nil {
				log.Printf("dmetric: restart setelah update otomatis: %v", err)
			}
		})
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	webURL := fmt.Sprintf("http://%s/", net.JoinHostPort(dialHost(bindHost), strconv.Itoa(effectivePort)))
	log.Printf("DMetric %s — web app di %s (data: %s)", model.Version, webURL, dataDir)

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("dmetric: menutup server: %v", err)
			}
		})
	}

	// A signal quits the desktop app cleanly too, which keeps Cmd+Q and
	// Ctrl+C behavior the same.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			if noWindow {
				shutdown()
			} else {
				mygo.App.Quit()
			}
		case <-ctx.Done():
		}
	}()

	if noWindow {
		select {
		case err := <-serveErr:
			if err != nil {
				log.Fatalf("server berhenti: %v", err)
			}
		case <-ctx.Done():
		}
		return
	}

	mygo.Theme.SetSource(mygo.ThemeLight)
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(buildMenu(webURL))
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "DMetric — Resource Monitor",
			URL:             webURL,
			Width:           1320,
			Height:          880,
			MinWidth:        980,
			MinHeight:       660,
			StateKey:        "main",
			BackgroundColor: "#f3f6fc",
		})
		win.OnClosed(func() { mygo.App.Quit() })
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { shutdown() })
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	shutdown()
}

func buildMenu(webURL string) *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "Tampilan", Submenu: []*mygo.MenuItem{
			{
				Label:       "Buka di Browser",
				Accelerator: "CmdOrCtrl+Shift+O",
				Click: func(*mygo.MenuItem, *mygo.Window) {
					if err := mygo.Shell.OpenExternal(webURL); err != nil {
						log.Printf("dmetric: membuka browser: %v", err)
					}
				},
			},
			{Role: mygo.RoleToggleFullScreen},
			{Role: mygo.RoleResetZoom},
			{Role: mygo.RoleZoomIn},
			{Role: mygo.RoleZoomOut},
		}},
		{Label: "Jendela", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize},
			{Role: mygo.RoleZoom},
		}},
	})
}

func defaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "dmetric-data"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "DMetric")
}

func dialHost(bind string) string {
	switch bind {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	}
	return bind
}

func flagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
