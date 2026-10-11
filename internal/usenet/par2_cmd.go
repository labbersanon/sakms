package usenet

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const par2CmdTimeout = 15 * time.Minute

// par2CmdRepair runs par2cmdline against the first PAR2 set in dir.
// go-newsgroups/par2 can report Complete while unrar still sees packed-data
// CRC errors (Leverage part11). par2 r is streaming and uses vol files on disk.
//
// Missing binary is not an error — the Debian image gains `par2` in this change.
func par2CmdRepair(dir string) error {
	bin, err := lookPath("par2")
	if err != nil {
		return fmt.Errorf("par2cmdline not installed")
	}
	index, err := firstPar2Index(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), par2CmdTimeout)
	defer cancel()
	cmd := unpackCommand(ctx, bin, "r", "-q", index)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("par2cmdline: %s", msg)
	}
	log.Printf("usenet: par2cmdline repaired %s", filepath.Base(index))
	return nil
}

func firstPar2Index(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var vol string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".par2") {
			continue
		}
		p := filepath.Join(dir, name)
		if !isPar2Candidate(p) {
			continue
		}
		lower := strings.ToLower(name)
		if !strings.Contains(lower, ".vol") {
			return p, nil
		}
		if vol == "" {
			vol = p
		}
	}
	if vol != "" {
		return vol, nil
	}
	return "", fmt.Errorf("par2cmdline: no .par2 set in %s", dir)
}
