package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const tuiStopFile = "tui-stop.json"

// RecordTUIStop is written by the provider's native Stop hook, not inferred
// from terminal output or the control plane's eventually consistent status.
func RecordTUIStop(dataDir string, at time.Time) error {
	if dataDir == "" {
		return os.ErrInvalid
	}
	payload, err := json.Marshal(struct {
		At int64 `json:"at"`
	}{At: at.UnixNano()})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dataDir, ".tui-stop-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dataDir, tuiStopFile))
}

func ReadTUIStop(dataDir string) (time.Time, error) {
	payload, err := os.ReadFile(filepath.Join(dataDir, tuiStopFile))
	if err != nil {
		return time.Time{}, err
	}
	var record struct {
		At int64 `json:"at"`
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, record.At), nil
}
