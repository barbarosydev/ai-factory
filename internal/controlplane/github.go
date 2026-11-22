package controlplane

import (
	"fmt"

	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/githubapp"
)

func newGitHubClient(cfg config.Config, existingSites func() ([]string, error)) (*githubapp.Client, *githubapp.InstallationAuth, error) {
	auth, err := githubapp.NewInstallationAuth(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubAppPEMBase64, "", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("init github app auth: %w", err)
	}
	gh, err := githubapp.NewGitHubClient(auth)
	if err != nil {
		return nil, nil, fmt.Errorf("init github client: %w", err)
	}
	c := githubapp.NewClient(gh, auth, cfg.GitHubOwner, cfg.GitHubRepo, existingSites)
	c.SetAllowedUsers(cfg.AllowedGitHubUsers)
	return c, auth, nil
}
