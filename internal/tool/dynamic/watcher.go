package dynamic

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/tool"
)

// ConfigLookup is the per-tool config resolver. Returns the
// map under `dynamic.<name>.config` in the user's
// ~/.p-chat/config.json, or nil if not set. Implemented as
// a closure so the watcher doesn't have to import the config
// package (which would create a cycle — config imports
// rules which imports tool).
type ConfigLookup func(toolName string) map[string]any

// Watch polls `dir` every `interval` for *.yaml files and
// keeps the tool.Registry in sync:
//
//   - new file → parse → Register
//   - existing file's mtime changed → re-parse → Re-register
//     (Unregister first, then Register, to avoid a stale
//     handler being called during the swap)
//   - deleted file → Unregister
//
// One malformed YAML is logged as a warning and skipped —
// it MUST NOT take down the other dynamic tools. A single
// bad file is a user typo, not a system fault.
//
// `onChange` fires after any successful register/unregister
// so the server can re-build the static system prompt's
// "Available Tools" table (the agent caches that on startup
// and only refreshes on Reload()).
//
// Returns a stop function. Call it from server.Shutdown
// to flush the goroutine. The watcher also exits when ctx
// is cancelled, whichever comes first.
func Watch(ctx context.Context, reg *tool.Registry, dir string, lookup ConfigLookup, interval time.Duration, onChange func()) (func(), error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			// User hasn't created the tools dir yet. That's
			// a normal state for a fresh install — create
			// the dir so the next `echo > ~/.p-chat/tools/x.yaml`
			// has somewhere to land.
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return func() {}, mkErr
			}
		} else {
			return func() {}, err
		}
	}

	// Per-file last-seen mtime. Keyed by absolute path so
	// two different dirs (project vs global) don't collide.
	state := map[string]time.Time{}
	// Reverse map: tool name → file path, so a delete can
	// unregister the right name.
	nameToPath := map[string]string{}
	var mu sync.Mutex

	// Prime: load whatever's already on disk so the
	// restart-after-edit case picks up edits made while
	// the server was down.
	if err := scanOnce(reg, dir, lookup, state, nameToPath, &mu); err != nil {
		log.Printf("[dynamic] initial scan warning: %v", err)
	}
	if onChange != nil {
		onChange()
	}

	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopped:
				return
			case <-ticker.C:
				if err := scanOnce(reg, dir, lookup, state, nameToPath, &mu); err != nil {
					log.Printf("[dynamic] scan warning: %v", err)
					continue
				}
				if onChange != nil {
					onChange()
				}
			}
		}
	}()
	return func() {
		close(stopped)
		<-done
	}, nil
}

// scanOnce does one pass of the dir: diff against `state`
// and apply the resulting register / unregister calls.
// Idempotent — the caller is the ticker. Always safe to
// call repeatedly with the same dir state.
func scanOnce(reg *tool.Registry, dir string, lookup ConfigLookup, state map[string]time.Time, nameToPath map[string]string, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// LiveSpecs accumulates the current set of valid
	// specs in this pass; we publish to the global
	// LookupSpec table at the end so the agent's
	// confirmTargetFor sees an atomic snapshot (no
	// half-registered tools).
	liveSpecs := map[string]Spec{}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		seen[full] = true
		prev, hasPrev := state[full]
		if hasPrev && mt.Equal(prev) {
			// Already up to date; carry the spec
			// forward to liveSpecs by re-loading it
			// (cheap — the YAML file is small and
			// parsing is in-memory). Skipping the
			// load would leave liveSpecs incomplete
			// and the next SetSpecs publish would
			// drop the spec.
			spec, perr := loadSpec(full, info.ModTime(), lookup)
			if perr != nil {
				log.Printf("[dynamic] skip %s: %v", filepath.Base(full), perr)
				continue
			}
			liveSpecs[spec.Name] = spec
			continue
		}
		// Re-load: either new file, or the mtime moved.
		// Either way we tear down the old registration
		// (if any) before re-registering, so a half-baked
		// edit can't leave the LLM calling a stale
		// handler.
		if prevName, ok := nameToPath[full]; ok {
			reg.Unregister(prevName)
		}
		spec, perr := loadSpec(full, info.ModTime(), lookup)
		if perr != nil {
			log.Printf("[dynamic] skip %s: %v", filepath.Base(full), perr)
			// Don't update state for a failed parse — a
			// later edit that fixes the YAML should still
			// be detected.
			continue
		}
		// Same YAML body but different mtime? Re-register
		// unconditionally so the user gets immediate
		// feedback that the edit "took". The Unregister
		// above already cleared the old name.
		reg.RegisterWithSource(spec.AsTool(), BuildDynamicHandler(spec), spec.Source)
		state[full] = mt
		nameToPath[full] = spec.Name
		liveSpecs[spec.Name] = spec
		log.Printf("[dynamic] registered %q from %s (source=%s)", spec.Name, filepath.Base(full), full)
	}
	// Deletions: a file we knew about that's no longer
	// in `seen` should be unregistered.
	for path, name := range nameToPath {
		if !seen[path] {
			reg.Unregister(name)
			delete(state, path)
			delete(nameToPath, path)
			log.Printf("[dynamic] unregistered %q (file removed)", name)
		}
	}
	// Publish the new spec snapshot for the agent's
	// confirm path to read. Done unconditionally — even
	// when liveSpecs is empty, the previous table is
	// replaced so a deleted tool can't keep prompting
	// from a stale confirm.
	SetSpecs(liveSpecs)
	return nil
}

// loadSpec reads the YAML file at `path` and returns the
// parsed Spec with Source / ModTime / Config fields
// populated. Parse errors are returned as-is so the caller
// can log them with the file name.
func loadSpec(path string, modTime time.Time, lookup ConfigLookup) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	spec, err := ParseSpec(data)
	if err != nil {
		return Spec{}, err
	}
	spec.Source = path
	spec.ModTime = modTime
	if lookup != nil {
		spec.Config = lookup(spec.Name)
	}
	return spec, nil
}
