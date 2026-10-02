package disc

import (
	"encoding/json"
	"fmt"
	"os"
)

func writeSidecar(m *Map) error {
	if m == nil {
		return fmt.Errorf("disc: nil map")
	}
	path := SidecarPath(m.Source)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func readSidecar(src string) (*Map, error) {
	data, err := os.ReadFile(SidecarPath(src))
	if err != nil {
		return nil, err
	}
	var m Map
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
