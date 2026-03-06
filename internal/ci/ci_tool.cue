package ci

import (
	"github.com/myitcvscratch/cue_user_funcs/internal/ci/repo"
	"github.com/myitcvscratch/cue_user_funcs/internal/ci/github"
)

command: gen: {
	workflows: repo.writeWorkflows & {#in: workflows: github.workflows}

	codereviewCfg: repo.writeCodereviewCfg
}
