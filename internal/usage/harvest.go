// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enclave/internal/config"
	"enclave/internal/model"
	"enclave/internal/util"
)

const (
	defaultStoreKey = "default"
	stateFilename   = "sources.json"
	lockName        = "usage.lock"
	// pendingGrace is how long a log must stay unchanged before a response
	// without a final count is archived as is. Streaming writes a log far
	// more often, so an older one belongs to an interrupted response.
	pendingGrace = 30 * time.Minute
	// slowNotice is when a harvest announces itself. The first harvest of a
	// large store reads every log once.
	slowNotice = 2 * time.Second
	// maxKeyBytes is well above the longest key a source builds.
	maxKeyBytes = 256
)

// Store identifies one tool config store. An empty Key is the default store.
type Store struct {
	Tool        string
	ProjectHash string
	Key         string
}

type Options struct {
	// Removing archives responses without a final count as they are, because
	// the stores are deleted next. Otherwise such a response waits until its
	// log has been unchanged for pendingGrace: another session may still be
	// writing it, and the archive keeps the first count it sees.
	Removing bool
	// Wait blocks while another process harvests. Otherwise the harvest is
	// skipped, which only delays archiving of stores that persist.
	Wait bool
	// Notify reports a wait or a slow harvest. Nil stays quiet.
	Notify func(format string, args ...any)
}

// harvestState records the size and mtime of every log already archived, so
// unchanged logs are not read again. A changed log is read in full and
// deduplicated by key.
type harvestState struct {
	Files map[string]fileStamp `json:"files"`
}

type fileStamp struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mtime"`
}

// Harvest archives usage from the given config stores and returns the number
// of new records. Errors on single logs do not stop the harvest. They are
// joined into the returned error.
func Harvest(home string, stores []Store, opts Options) (int, error) {
	return harvest(home, stores, opts, false)
}

// HarvestAll archives usage from every config store of every tool with a
// registered source.
func HarvestAll(home string, opts Options) (int, error) {
	return harvest(home, discoverStores(home), opts, true)
}

// StoresUnder lists the config stores located in any of dirs, for archiving
// usage before the dirs are removed.
func StoresUnder(home string, dirs []string) []Store {
	var stores []Store
	for _, store := range discoverStores(home) {
		storeDir := config.HostStoreConfigDir(home, store.Tool, store.ProjectHash, store.Key)
		for _, dir := range dirs {
			dir = filepath.Clean(dir)
			if storeDir == dir || strings.HasPrefix(storeDir, dir+string(filepath.Separator)) {
				stores = append(stores, store)
				break
			}
		}
	}
	return stores
}

func discoverStores(home string) []Store {
	var stores []Store
	for _, hash := range subdirs(config.HostProjectsDir(home)) {
		if !model.IsHashSegment(hash) {
			continue
		}
		for _, tool := range Tools() {
			for _, key := range subdirs(config.HostStoreConfigRootDir(home, tool, hash)) {
				stores = append(stores, Store{Tool: tool, ProjectHash: hash, Key: key})
			}
		}
	}
	return stores
}

func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func harvest(home string, stores []Store, opts Options, pruneAll bool) (int, error) {
	if strings.TrimSpace(home) == "" {
		return 0, errors.New("home directory is not set")
	}
	notify := opts.Notify
	if notify == nil {
		notify = func(string, ...any) {}
	}
	release, err := lockArchive(home, opts.Wait, notify)
	if err != nil || release == nil {
		return 0, err
	}
	defer release()
	notice := time.AfterFunc(slowNotice, func() {
		notify("Archiving token usage from session logs...")
	})
	defer notice.Stop()

	dir := config.HostUsageDir(home)
	h := harvester{
		home:     home,
		removing: opts.Removing,
		archive:  newArchive(dir),
		state:    loadState(filepath.Join(dir, stateFilename)),
		seen:     map[string]struct{}{},
		projects: map[string]string{},
	}
	for _, store := range stores {
		h.harvestStore(store)
	}
	if h.fatal == nil && pruneAll {
		for path := range h.state.Files {
			if _, ok := h.seen[path]; !ok {
				delete(h.state.Files, path)
			}
		}
	}
	// The state must not claim records that a crash could still lose.
	if err := h.archive.sync(); err != nil {
		h.fatal = errors.Join(h.fatal, err)
	} else if err := saveState(filepath.Join(dir, stateFilename), h.state); err != nil {
		h.addErr(fmt.Errorf("save usage state: %w", err))
	}
	if h.fatal != nil {
		h.errs = append([]error{h.fatal}, h.errs...)
	}
	return h.added, errors.Join(h.errs...)
}

// lockArchive returns a nil release without an error when it does not wait
// and another process holds the lock.
func lockArchive(home string, wait bool, notify func(string, ...any)) (func(), error) {
	path := config.HostLockPath(home, lockName)
	if wait {
		release, err := util.AcquireFileLock(path, func() {
			notify("Waiting for another enclave process to finish archiving token usage.")
		})
		if err != nil {
			return nil, fmt.Errorf("lock usage archive: %w", err)
		}
		return release, nil
	}
	release, ok, err := util.TryAcquireFileLock(path)
	if err != nil {
		return nil, fmt.Errorf("lock usage archive: %w", err)
	}
	if !ok {
		return nil, nil
	}
	return release, nil
}

type harvester struct {
	home     string
	removing bool
	archive  *archive
	state    harvestState
	seen     map[string]struct{}
	projects map[string]string
	added    int
	errs     []error
	// fatal is an archive write failure. Later stores are skipped because
	// every write would fail the same way.
	fatal error
}

// addErr records an error that does not stop the harvest. Its message can
// name files in an agent-writable store, so it is made safe to print.
func (h *harvester) addErr(err error) {
	h.errs = append(h.errs, displaySafeError{err})
}

type displaySafeError struct{ err error }

func (e displaySafeError) Error() string { return util.DisplaySafe(e.err.Error()) }
func (e displaySafeError) Unwrap() error { return e.err }

func (h *harvester) harvestStore(store Store) {
	source, ok := sourceFor(store.Tool)
	if !ok || h.fatal != nil {
		return
	}
	key := strings.TrimSpace(store.Key)
	if key == "" {
		key = defaultStoreKey
	}
	if !safeSegment(store.Tool) || !safeSegment(store.ProjectHash) || !safeSegment(key) {
		h.addErr(fmt.Errorf("invalid store %s/%s/%s", store.Tool, store.ProjectHash, key))
		return
	}
	storeDir := config.HostStoreConfigDir(h.home, store.Tool, store.ProjectHash, key)
	base := Record{
		Tool:        store.Tool,
		ProjectHash: store.ProjectHash,
		Project:     h.project(store.ProjectHash),
		Store:       key,
	}
	h.walkStore(source, storeDir, base)
	if h.fatal != nil {
		return
	}
	prefix := storeDir + string(filepath.Separator)
	for path := range h.state.Files {
		if _, ok := h.seen[path]; !ok && strings.HasPrefix(path, prefix) {
			delete(h.state.Files, path)
		}
	}
}

// walkStore reads the logs through an os.Root: the store is agent-writable,
// and a directory swapped for a symlink mid-walk must not lead the host
// outside it.
func (h *harvester) walkStore(source Source, storeDir string, base Record) {
	root, err := os.OpenRoot(storeDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.addErr(err)
		}
		return
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()
	for _, dir := range source.Dirs() {
		err := fs.WalkDir(fsys, dir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					h.addErr(err)
				}
				return nil
			}
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			h.harvestFile(source, root, name, filepath.Join(storeDir, filepath.FromSlash(name)), base)
			if h.fatal != nil {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			h.addErr(err)
		}
	}
}

func (h *harvester) harvestFile(source Source, root *os.Root, name string, path string, base Record) {
	file, err := root.OpenFile(filepath.FromSlash(name), logOpenFlags, 0)
	if err != nil {
		h.addErr(err)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		h.addErr(err)
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	h.seen[path] = struct{}{}
	stamp := fileStamp{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	if previous, ok := h.state.Files[path]; ok && previous == stamp {
		return
	}
	records, err := source.Parse(file)
	if err != nil {
		h.addErr(fmt.Errorf("parse %s: %w", path, err))
		return
	}
	settled := h.removing || time.Since(info.ModTime()) > pendingGrace
	deferred := false
	valid := records[:0]
	for _, record := range records {
		if record.Key == "" || record.Timestamp.IsZero() {
			continue
		}
		if record.Pending && !settled {
			deferred = true
			continue
		}
		record.Key = base.Tool + ":" + archiveKey(record.Key)
		record.Tool = base.Tool
		record.ProjectHash = base.ProjectHash
		record.Project = base.Project
		record.Store = base.Store
		record.dropOversized()
		valid = append(valid, record)
	}
	added, err := h.archive.add(valid)
	h.added += added
	if err != nil {
		h.fatal = err
		return
	}
	if deferred {
		// The next harvest reads the log again to archive the final count.
		delete(h.state.Files, path)
		return
	}
	h.state.Files[path] = stamp
}

func (h *harvester) project(hash string) string {
	if dir, ok := h.projects[hash]; ok {
		return dir
	}
	dir := config.ReadProjectMarker(h.home, hash)
	h.projects[hash] = dir
	return dir
}

// archiveKey keeps keys short and free of characters that need JSON escaping,
// which lineKey relies on. Keys from the agent-writable logs that are longer
// than maxKeyBytes or contain any such character are replaced by their hash.
func archiveKey(key string) string {
	if len(key) <= maxKeyBytes && !strings.ContainsFunc(key, func(r rune) bool { return !isKeyRune(r) }) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256-" + hex.EncodeToString(sum[:])
}

func isKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '-' || r == '_' || r == '.' || r == ':'
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`)
}

func loadState(path string) harvestState {
	state := harvestState{Files: map[string]fileStamp{}}
	data, err := os.ReadFile(path) // #nosec G304 -- path is resolved from enclave's own state directory.
	if err != nil {
		return state
	}
	// A corrupt state only costs a full re-read. Deduplication keeps the
	// archive correct.
	if json.Unmarshal(data, &state) != nil || state.Files == nil {
		state.Files = map[string]fileStamp{}
	}
	return state
}

func saveState(path string, state harvestState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, data, 0o600)
}
