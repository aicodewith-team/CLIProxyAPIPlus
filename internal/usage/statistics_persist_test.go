package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestStatisticsSnapshotFileRoundTrip(t *testing.T) {
	prevStatsEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(prevStatsEnabled)
	})

	source := NewRequestStatistics()
	requestedAt := time.Date(2026, 11, 17, 10, 30, 0, 0, time.UTC)
	source.Record(context.Background(), coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-4o",
		RequestedAt: requestedAt,
		Latency:     250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     9,
			OutputTokens:    5,
			ReasoningTokens: 2,
			CachedTokens:    3,
			TotalTokens:     14,
		},
	})
	exported := source.Snapshot()
	if exported.TotalRequests != 1 || exported.TotalTokens != 14 {
		t.Fatalf("exported totals = requests:%d tokens:%d, want 1 and 14", exported.TotalRequests, exported.TotalTokens)
	}
	if exported.TokensByDay["2026-11-17"] != 14 {
		t.Fatalf("tokens_by_day = %v, want 14 on 2026-11-17", exported.TokensByDay)
	}

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	if err := writeStatisticsSnapshot(path, exported); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	loaded, ok, err := readStatisticsSnapshot(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !ok {
		t.Fatal("read snapshot: file was not found")
	}

	target := NewRequestStatistics()
	result := target.RestoreSnapshot(loaded)
	if result.Added != 1 {
		t.Fatalf("restore added = %d, want 1", result.Added)
	}
	restored := target.Snapshot()
	if restored.TotalRequests != 1 || restored.SuccessCount != 1 || restored.FailureCount != 0 || restored.TotalTokens != 14 {
		t.Fatalf("restored totals = %+v, want requests=1 success=1 tokens=14", restored)
	}
	if restored.TokensByDay["2026-11-17"] != 14 {
		t.Fatalf("restored tokens_by_day = %v, want 14 on 2026-11-17", restored.TokensByDay)
	}
	details := restored.APIs["openai"].Models["gpt-4o"].Details
	if len(details) != 1 {
		t.Fatalf("restored details len = %d, want 1", len(details))
	}
	detail := details[0]
	if !detail.Timestamp.Equal(requestedAt) {
		t.Fatalf("detail timestamp = %v, want %v", detail.Timestamp, requestedAt)
	}
	if detail.LatencyMs != 250 || detail.Failed {
		t.Fatalf("detail = %+v, want latency_ms=250 failed=false", detail)
	}
	if detail.Tokens.InputTokens != 9 || detail.Tokens.OutputTokens != 5 || detail.Tokens.ReasoningTokens != 2 || detail.Tokens.CachedTokens != 3 || detail.Tokens.TotalTokens != 14 {
		t.Fatalf("detail tokens = %+v, want input=9 output=5 reasoning=2 cached=3 total=14", detail.Tokens)
	}
}

func TestStatisticsSnapshotCorruptFileFallback(t *testing.T) {
	dir := t.TempDir()
	corruptPath := filepath.Join(dir, "usage-statistics.json")
	if err := os.WriteFile(corruptPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}

	if _, ok, err := readStatisticsSnapshot(corruptPath); err == nil || ok {
		t.Fatalf("corrupt snapshot: ok=%v err=%v, want an error", ok, err)
	}

	emptyPath := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(emptyPath, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty snapshot: %v", err)
	}
	if _, ok, err := readStatisticsSnapshot(emptyPath); err == nil || ok {
		t.Fatalf("empty snapshot: ok=%v err=%v, want an error", ok, err)
	}

	missingPath := filepath.Join(dir, "missing.json")
	snapshot, ok, err := readStatisticsSnapshot(missingPath)
	if err != nil || ok {
		t.Fatalf("missing snapshot: ok=%v err=%v, want missing without error", ok, err)
	}
	if snapshot.TotalRequests != 0 || snapshot.TotalTokens != 0 || len(snapshot.APIs) != 0 {
		t.Fatalf("missing snapshot should be empty, got %+v", snapshot)
	}

	// A corrupt file must not fail startup or leave partial state behind.
	target := NewRequestStatistics()
	if _, ok, err := readStatisticsSnapshot(corruptPath); err == nil || ok {
		t.Fatalf("expected corrupt snapshot to be rejected")
	}
	restored := target.Snapshot()
	if restored.TotalRequests != 0 || restored.TotalTokens != 0 || len(restored.APIs) != 0 || len(restored.TokensByDay) != 0 {
		t.Fatalf("store after corrupt fallback = %+v, want empty", restored)
	}
}
