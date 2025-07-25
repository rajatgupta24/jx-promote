package promote

import (
	"fmt"
	"os"

	"github.com/jenkins-x-plugins/jx-promote/pkg/environments"
	"github.com/jenkins-x/jx-helpers/v3/pkg/requirements"

	jxcore "github.com/jenkins-x/jx-api/v4/pkg/apis/core/v4beta1"

	"github.com/jenkins-x-plugins/jx-promote/pkg/promoteconfig"
	"github.com/jenkins-x-plugins/jx-promote/pkg/rules"
	"github.com/jenkins-x-plugins/jx-promote/pkg/rules/factory"
	"github.com/jenkins-x/jx-helpers/v3/pkg/gitclient"
	"github.com/jenkins-x/jx-helpers/v3/pkg/gitclient/gitconfig"
)

func (o *Options) PromoteViaPullRequest(envs []*jxcore.EnvironmentConfig, releaseInfo *ReleaseInfo, draftPR bool) error {
	version := o.Version
	versionName := version
	if versionName == "" {
		versionName = "latest"
	}
	app := o.Application

	source := "promote-" + app + "-" + versionName
	var labels []string

	// TODO: Support more labels. I'm thinking owner...
	for _, env := range envs {
		envName := env.Key
		source += "-" + envName
		labels = append(labels, "env/"+envName)
	}

	var dependencyLabel = "dependency/" + releaseInfo.FullAppName

	if len(dependencyLabel) > 49 {
		dependencyLabel = dependencyLabel[:49]
	}
	labels = append(labels, dependencyLabel)

	if o.ReusePullRequest && o.PullRequestFilter == nil {
		o.PullRequestFilter = &environments.PullRequestFilter{Labels: labels}
		// Clearing so that it can be set for the correct environment on next call
		defer func() { o.PullRequestFilter = nil }()
	}

	comment := "this commit will trigger a pipeline to [generate the actual kubernetes resources to perform the promotion](https://jenkins-x.io/v3/about/how-it-works/#promotion) which will create a second commit on this Pull Request before it can merge"

	if draftPR {
		labels = append(labels, "do-not-merge/hold")
	}

	o.CommitTitle = fmt.Sprintf("chore: promote %s to version %s", app, versionName)
	o.CommitMessage = comment
	if o.AddChangelog != "" {
		changelog, err := os.ReadFile(o.AddChangelog)
		if err != nil {
			return fmt.Errorf("failed to read changelog file %s: %w", o.AddChangelog, err)
		}
		o.CommitChangelog = string(changelog)
	}

	envDir := ""
	if o.CloneDir != "" {
		envDir = o.CloneDir
	}

	o.Function = func() error {
		dir := o.OutDir

		for _, env := range envs {
			promoteNS := EnvironmentNamespace(env)
			promoteConfig, _, err := promoteconfig.Discover(dir, promoteNS)
			if err != nil {
				return fmt.Errorf("failed to discover the PromoteConfig in dir %s: %w", dir, err)
			}

			r := &rules.PromoteRule{
				TemplateContext: rules.TemplateContext{
					GitURL:            "",
					Version:           o.Version,
					AppName:           o.Application,
					ChartAlias:        o.Alias,
					Namespace:         o.Namespace,
					HelmRepositoryURL: o.HelmRepositoryURL,
					ReleaseName:       o.ReleaseName,
				},
				Dir:           dir,
				Config:        *promoteConfig,
				DevEnvContext: &o.DevEnvContext,
			}

			// lets check if we need the apps git URL
			if promoteConfig.Spec.FileRule != nil || promoteConfig.Spec.KptRule != nil {
				if o.AppGitURL == "" {
					_, gitConf, err := gitclient.FindGitConfigDir("")
					if err != nil {
						return fmt.Errorf("failed to find git config dir: %w", err)
					}
					o.AppGitURL, err = gitconfig.DiscoverUpstreamGitURL(gitConf, true)
					if err != nil {
						return fmt.Errorf("failed to discover application git URL: %w", err)
					}
					if o.AppGitURL == "" {
						return fmt.Errorf("could not to discover application git URL")
					}
				}
				r.GitURL = o.AppGitURL
			}

			fn := factory.NewFunction(r)
			if fn == nil {
				return fmt.Errorf("could not create rule function ")
			}
			err = fn(r)
			if err != nil {
				return fmt.Errorf("failed to promote to %s: %w", env.Key, err)
			}
		}
		return nil
	}

	if releaseInfo.PullRequestInfo != nil {
		o.PullRequestNumber = releaseInfo.PullRequestInfo.Number
	}
	env := envs[0]
	gitURL := requirements.EnvironmentGitURL(o.DevEnvContext.Requirements, env.Key)
	if gitURL == "" {
		if env.RemoteCluster {
			return fmt.Errorf("no git URL for remote cluster %s", env.Key)
		}

		// lets default to the git repository for the dev environment for local clusters
		gitURL = requirements.EnvironmentGitURL(o.DevEnvContext.Requirements, "dev")
		if gitURL == "" {
			return fmt.Errorf("no git URL for dev environment")
		}
	}
	autoMerge := o.AutoMerge
	if draftPR {
		autoMerge = false
	}
	info, err := o.Create(gitURL, envDir, labels, autoMerge)
	releaseInfo.PullRequestInfo = info
	return err
}
