package light

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TransferManager tracks in-flight transfers and persists completed/failed/
// cancelled transfers to ~/.light/history.json (JSON — deliberately simpler than
// SQLite to avoid any CGO/mobile cross-compile risk).
type TransferManager struct {
	mu      sync.RWMutex
	active  map[string]*Transfer
	history []*Transfer
}

func NewTransferManager() *TransferManager {
	m := &TransferManager{active: make(map[string]*Transfer)}
	m.loadHistory()
	return m
}

func (m *TransferManager) loadHistory() {
	data, err := os.ReadFile(filepath.Join(configDir(), "history.json"))
	if err != nil {
		return
	}
	var h []*Transfer
	if json.Unmarshal(data, &h) == nil {
		m.history = h
	}
}

func (m *TransferManager) saveHistory() {
	m.mu.RLock()
	h := m.history
	m.mu.RUnlock()
	data, _ := json.MarshalIndent(h, "", "  ")
	_ = os.WriteFile(filepath.Join(configDir(), "history.json"), data, 0o644)
}

func (m *TransferManager) RecordTransfer(t *Transfer) {
	m.mu.Lock()
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}
	m.active[t.ID] = t
	m.mu.Unlock()
}

func (m *TransferManager) UpdateProgress(id string, transferred, speed int64) {
	m.mu.Lock()
	if t, ok := m.active[id]; ok {
		t.Transferred = transferred
		t.Speed = speed
		if t.Size > 0 {
			t.Percent = int(transferred * 100 / t.Size)
		}
	}
	m.mu.Unlock()
}

func (m *TransferManager) finalize(id, status, filePath, checksum, errMsg string) {
	m.mu.Lock()
	t, ok := m.active[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	t.Status = TransferStatus(status)
	t.FilePath = filePath
	t.Checksum = checksum
	t.Error = errMsg
	if status == string(StatusCompleted) {
		t.Transferred = t.Size
		t.Percent = 100
	}
	now := time.Now()
	t.CompletedAt = &now
	m.history = append([]*Transfer{t}, m.history...)
	if len(m.history) > 500 {
		m.history = m.history[:500]
	}
	delete(m.active, id)
	m.mu.Unlock()
	m.saveHistory()
}

func (m *TransferManager) Complete(id, filePath, checksum string) {
	m.finalize(id, string(StatusCompleted), filePath, checksum, "")
}

func (m *TransferManager) Fail(id, errMsg string) {
	m.finalize(id, string(StatusFailed), "", "", errMsg)
}

func (m *TransferManager) Cancel(id string) {
	m.finalize(id, string(StatusCancelled), "", "", "cancelled")
}

func (m *TransferManager) Pause(id string) {
	m.mu.Lock()
	if t, ok := m.active[id]; ok {
		t.Status = StatusPaused
	}
	m.mu.Unlock()
}

func (m *TransferManager) Resume(id string) {
	m.mu.Lock()
	if t, ok := m.active[id]; ok {
		t.Status = StatusActive
	}
	m.mu.Unlock()
}

func (m *TransferManager) GetAll() []*Transfer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Transfer, 0, len(m.active))
	for _, t := range m.active {
		out = append(out, t)
	}
	return out
}

func (m *TransferManager) GetHistory(limit int) []*Transfer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit >= len(m.history) {
		return m.history
	}
	return m.history[:limit]
}

func (m *TransferManager) ClearHistory() {
	m.mu.Lock()
	m.history = nil
	m.mu.Unlock()
	m.saveHistory()
}
