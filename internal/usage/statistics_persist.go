package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
)

const (
	usageStatisticsFileName        = "usage-statistics.json"
	usageStatisticsPersistInterval = 10 * time.Second
)

var (
	defaultStatisticsDirty atomic.Bool

	statisticsPersistMu      sync.Mutex
	statisticsPersistPath    string
	statisticsPersistLoaded  bool
	statisticsPersistRunning bool
	statisticsPersistStop    chan struct{}
	statisticsPersistDone    chan struct{}
)

// ConfigureStatisticsPersistence sets the snapshot path under the log directory,
// loads an existing file once when statistics are enabled, and starts the
// debounce saver. Missing or unwritable directories are skipped without failing
// process startup. Later reloads should only toggle SetStatisticsEnabled.
func ConfigureStatisticsPersistence(cfg *config.Config) {
	path := statisticsPersistencePath(cfg)
	statisticsPersistMu.Lock()
	defer statisticsPersistMu.Unlock()
	statisticsPersistPath = path
	if !statisticsEnabled.Load() {
		return
	}
	loadDefaultStatisticsLocked()
	startStatisticsPersisterLocked()
}

// FlushAndStopStatisticsPersistence writes a dirty snapshot (if any) and stops
// the background saver. It is safe to call more than once.
func FlushAndStopStatisticsPersistence() {
	statisticsPersistMu.Lock()
	defer statisticsPersistMu.Unlock()
	stopStatisticsPersisterLocked()
}

func syncStatisticsPersister(enabled bool) {
	statisticsPersistMu.Lock()
	defer statisticsPersistMu.Unlock()
	if enabled {
		loadDefaultStatisticsLocked()
		startStatisticsPersisterLocked()
		return
	}
	stopStatisticsPersisterLocked()
}

func statisticsPersistencePath(cfg *config.Config) string {
	dir := strings.TrimSpace(internallogging.ResolveLogDirectory(cfg))
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, usageStatisticsFileName)
}

func loadDefaultStatisticsLocked() {
	if statisticsPersistLoaded || statisticsPersistPath == "" {
		return
	}
	statisticsPersistLoaded = true
	snapshot, ok, err := readStatisticsSnapshot(statisticsPersistPath)
	if err != nil {
		log.Warnf("usage statistics: ignoring snapshot %s: %v", statisticsPersistPath, err)
		return
	}
	if !ok {
		return
	}
	defaultRequestStatistics.RestoreSnapshot(snapshot)
	defaultStatisticsDirty.Store(false)
}

func startStatisticsPersisterLocked() {
	if statisticsPersistRunning || statisticsPersistPath == "" || !statisticsEnabled.Load() {
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	statisticsPersistStop = stopCh
	statisticsPersistDone = doneCh
	statisticsPersistRunning = true
	go runStatisticsPersister(statisticsPersistPath, stopCh, doneCh)
}

func stopStatisticsPersisterLocked() {
	if statisticsPersistRunning {
		close(statisticsPersistStop)
		done := statisticsPersistDone
		path := statisticsPersistPath
		statisticsPersistRunning = false
		statisticsPersistStop = nil
		statisticsPersistDone = nil
		statisticsPersistMu.Unlock()
		<-done
		flushStatisticsSnapshot(path)
		statisticsPersistMu.Lock()
		return
	}
	flushStatisticsSnapshot(statisticsPersistPath)
}

func runStatisticsPersister(path string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(usageStatisticsPersistInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			flushStatisticsSnapshot(path)
		case <-stop:
			flushStatisticsSnapshot(path)
			return
		}
	}
}

func flushStatisticsSnapshot(path string) {
	if path == "" || !defaultStatisticsDirty.Load() {
		return
	}
	if err := writeStatisticsSnapshot(path, defaultRequestStatistics.Snapshot()); err != nil {
		log.Warnf("usage statistics: failed to persist snapshot %s: %v", path, err)
		return
	}
	defaultStatisticsDirty.Store(false)
}

func readStatisticsSnapshot(path string) (StatisticsSnapshot, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatisticsSnapshot{}, false, nil
		}
		return StatisticsSnapshot{}, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return StatisticsSnapshot{}, false, errors.New("file is empty")
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return StatisticsSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func writeStatisticsSnapshot(path string, snapshot StatisticsSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return errors.New("usage statistics snapshot directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "usage-statistics-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	removeTmp = false
	return nil
}
