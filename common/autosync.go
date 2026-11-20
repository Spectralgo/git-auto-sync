package common

import "errors"

// AutoSync preserves the legacy entrypoint while sharing the same checkout lock
// and merge-based engine as the one-shot CLI.
func AutoSync(repoConfig RepoConfig) error {
	result := Sync(repoConfig)
	if result.State != "synced" {
		return errors.New(result.Error)
	}
	return nil
}
