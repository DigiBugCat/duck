// Package folder holds the laptop-side per-folder sync policy store and the
// riskiness classifier the bare-`duck` flow consults to decide whether to
// auto-mirror cwd to the hub. Both are laptop-side only (no hub contact): the
// store persists a tilde-path -> "sync"|"never" map in the duck config, and the
// classifier does a cheap, bounded look at the local tree so a multi-GB or
// home-directory mirror is never started silently (DESIGN risk #2).
package folder

import "github.com/DigiBugCat/duck/internal/config"

// Policy is a remembered per-folder sync decision. PolicySync auto-mirrors the
// folder; PolicyNever keeps it sync-free; the empty string means unknown (no
// remembered decision — the flow classifies and may prompt).
type Policy = string

const (
	PolicySync  Policy = "sync"
	PolicyNever Policy = "never"
)

// Store reads and writes per-folder sync policies, persisted in the duck config
// TOML's [folders] table keyed by tilde-form dir. The zero value is usable; it
// delegates to config.Load/config.Save so a Set never clobbers the rest of the
// config (Hub, AutoName, …).
type Store struct{}

// NewStore returns a policy store backed by the duck config file.
func NewStore() *Store { return &Store{} }

// Get returns the remembered policy for the tilde-form dir and whether one was
// stored. An unknown dir (or any load error) reports ok=false so the caller
// falls through to classification.
func (s *Store) Get(dir string) (Policy, bool) {
	c, err := config.Load()
	if err != nil {
		return "", false
	}
	if c.Folders == nil {
		return "", false
	}
	p, ok := c.Folders[dir]
	if !ok || p == "" {
		return "", false
	}
	return p, true
}

// Set records policy for the tilde-form dir, loading the whole config and
// re-saving so unrelated fields are preserved.
func (s *Store) Set(dir string, policy Policy) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.Folders == nil {
		c.Folders = map[string]string{}
	}
	c.Folders[dir] = policy
	return config.Save(c)
}

// Forget drops any remembered policy for the tilde-form dir. A no-op when the
// dir is unknown.
func (s *Store) Forget(dir string) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.Folders == nil {
		return nil
	}
	delete(c.Folders, dir)
	return config.Save(c)
}
