package common

import (
	"errors"
	"github.com/ztrue/tracerr"
)

func push(repoConfig RepoConfig) error {
	bi, err := fetchBranchInfo(repoConfig.RepoPath)
	if err != nil {
		return tracerr.Wrap(err)
	}

	if bi.UpstreamBranch == "" || bi.UpstreamRemote == "" {
		return errors.New("branch has no remote upstream")
	}

	_, err = GitCommand(repoConfig, []string{"push", bi.UpstreamRemote, "HEAD:refs/heads/" + bi.UpstreamBranch})
	if err != nil {
		return tracerr.Wrap(err)
	}

	return nil
}
