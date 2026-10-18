package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Diff returns a deterministic unified-style diff of all pending changes.
func (w *Workspace) Diff() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return "", err
	}
	names := make([]string, 0, len(w.staged))
	for name := range w.staged {
		names = append(names, name)
	}
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		change := w.staged[name]
		oldName := "/dev/null"
		if change.exists {
			oldName = "a/" + name
		}
		newName := "b/" + name
		if !change.exists {
			oldName = "/dev/null"
		}
		fmt.Fprintf(&output, "--- %s\n+++ %s\n", oldName, newName)
		oldLines := diffLines(change.original)
		newLines := diffLines(change.content)
		oldStart, newStart := 1, 1
		if len(oldLines) == 0 {
			oldStart = 0
		}
		if len(newLines) == 0 {
			newStart = 0
		}
		fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStart, len(oldLines), newStart, len(newLines))
		writeDiffLines(&output, '-', change.original, oldLines)
		writeDiffLines(&output, '+', change.content, newLines)
	}
	return output.String(), nil
}
func writeDiffLines(output *strings.Builder, prefix byte, data []byte, lines [][]byte) {
	for index, line := range lines {
		output.WriteByte(prefix)
		output.Write(line)
		output.WriteByte('\n')
		if index == len(lines)-1 && !bytes.HasSuffix(data, []byte("\n")) {
			output.WriteString("\\ No newline at end of file\n")
		}
	}
}

func diffLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(data, []byte("\n"))
	if bytes.HasSuffix(data, []byte("\n")) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Apply preflights every staged original before any disk replacement.
func (w *Workspace) Apply() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return err
	}
	if len(w.staged) == 0 {
		return nil
	}
	names := make([]string, 0, len(w.staged))
	for name := range w.staged {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := w.checkOriginal(name, w.staged[name]); err != nil {
			return fmt.Errorf("apply conflict at %s: %w", name, err)
		}
	}
	temps := make(map[string]string, len(names))
	cleanup := func() {
		for _, temp := range temps {
			_ = w.root.Remove(temp)
		}
	}
	for _, name := range names {
		parent := filepath.Dir(filepath.FromSlash(name))
		if parent != "." {
			if err := w.root.MkdirAll(parent, 0755); err != nil {
				cleanup()
				return fmt.Errorf("prepare directory for %s: %w", name, err)
			}
		}
		change := w.staged[name]
		temp, err := w.createTemp(parent, change.content, change.mode)
		if err != nil {
			cleanup()
			return fmt.Errorf("prepare replacement for %s: %w", name, err)
		}
		temps[name] = temp
	}
	applied := make([]string, 0, len(names))
	for _, name := range names {
		if err := w.checkOriginal(name, w.staged[name]); err != nil {
			cleanup()
			w.removeApplied(applied)
			return fmt.Errorf("apply conflict at %s after applying [%s]: %w", name, strings.Join(applied, ", "), err)
		}
		if err := w.root.Rename(temps[name], filepath.FromSlash(name)); err != nil {
			cleanup()
			w.removeApplied(applied)
			return fmt.Errorf("apply failed at %s after applying [%s]: %w", name, strings.Join(applied, ", "), err)
		}
		delete(temps, name)
		applied = append(applied, name)
	}
	w.removeApplied(applied)
	return nil
}

func (w *Workspace) removeApplied(names []string) {
	for _, name := range names {
		delete(w.staged, name)
	}
}

func (w *Workspace) checkOriginal(name string, change stagedFile) error {
	path := filepath.FromSlash(name)
	info, err := w.checkComponents(path, !change.exists)
	if !change.exists {
		if err == nil && info == nil {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("new-file target now exists")
	}
	if err != nil {
		return err
	}
	if info == nil {
		return fs.ErrNotExist
	}
	if !info.Mode().IsRegular() {
		return errors.New("target is no longer a regular file")
	}
	if info.Size() != int64(len(change.original)) {
		return errors.New("original size changed")
	}
	current, gotInfo, err := w.readDisk(path)
	if err != nil {
		return err
	}
	if !gotInfo.ModTime().Equal(info.ModTime()) {
		return errors.New("original modification time changed")
	}
	if !bytes.Equal(current, change.original) {
		return errors.New("original contents changed")
	}
	return nil
}

func (w *Workspace) createTemp(parent string, content []byte, mode fs.FileMode) (string, error) {
	for attempts := 0; attempts < 10; attempts++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".workspace-stage-" + hex.EncodeToString(random[:])
		if parent != "." {
			name = filepath.Join(parent, name)
		}
		file, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err = file.Write(content); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = w.root.Chmod(name, mode.Perm())
		}
		if err != nil {
			_ = w.root.Remove(name)
			return "", err
		}
		return name, nil
	}
	return "", errors.New("could not allocate temporary replacement")
}

// Reject discards all staged changes without touching the project tree.
func (w *Workspace) Reject() { w.mu.Lock(); defer w.mu.Unlock(); clear(w.staged) }
