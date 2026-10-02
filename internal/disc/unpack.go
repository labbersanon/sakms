package disc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Options control Unpack. SkipDelete is for tests that must keep the source.
// OnlyNames, when set, extracts only those PlanWorks names (t02, t01c07).
type Options struct {
	OnProgress func(done, total int)
	SkipDelete bool
	OnlyNames  []string
}

// Unpack probes src, writes a sidecar, extracts feature MKVs next to the
// ISO, then deletes the ISO only when every planned output exists.
func Unpack(ctx context.Context, src string, opts Options) (*Result, error) {
	if !IsDiscImage(src) {
		return nil, ErrNotDisc
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, ErrNotDisc
	}
	if _, err := lookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("%w: ffmpeg", ErrToolMissing)
	}
	if _, err := lookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("%w: ffprobe", ErrToolMissing)
	}

	volume := volumeOf(ctx, src)
	titles, err := probeTitles(ctx, src)
	if err != nil {
		return nil, err
	}
	titles = AssignRoles(titles)
	works := PlanWorks(titles)
	if len(opts.OnlyNames) > 0 {
		allow := map[string]bool{}
		for _, n := range opts.OnlyNames {
			allow[n] = true
		}
		filtered := works[:0]
		for _, w := range works {
			if allow[w.Name] {
				filtered = append(filtered, w)
			}
		}
		works = filtered
	}
	if len(works) == 0 {
		return nil, ErrNoFeatures
	}

	m := &Map{
		Version: mapVersion,
		Volume:  volume,
		Source:  src,
		Titles:  titles,
	}
	if err := writeSidecar(m); err != nil {
		return nil, fmt.Errorf("writing sidecar: %w", err)
	}
	if opts.OnProgress != nil {
		opts.OnProgress(0, len(works))
	}

	dir := filepath.Dir(src)
	var outputs []Output
	for i, w := range works {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		dest := outputPath(dir, volume, w.Name)
		if err := extractWork(ctx, src, w, dest, volume); err != nil {
			_ = writeSidecar(m)
			return nil, fmt.Errorf("%w: %v", ErrIncomplete, err)
		}
		out := Output{Path: dest, Title: w.Title, Chapter: w.ChapterStart, Role: w.Role}
		outputs = append(outputs, out)
		m.Outputs = outputs
		if err := writeSidecar(m); err != nil {
			return nil, err
		}
		if opts.OnProgress != nil {
			opts.OnProgress(i+1, len(works))
		}
	}

	deleted := false
	if !opts.SkipDelete {
		if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("extract ok but deleting ISO failed: %w", err)
		}
		deleted = true
		m.Deleted = true
		if err := writeSidecar(m); err != nil {
			return nil, err
		}
	}

	paths := make([]string, len(outputs))
	for i, o := range outputs {
		paths[i] = o.Path
	}
	return &Result{Map: m, Outputs: paths, DeletedSource: deleted}, nil
}
