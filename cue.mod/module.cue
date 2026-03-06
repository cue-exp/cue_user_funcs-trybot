module: "github.com/cue-exp/cue_user_funcs"
language: {
	version: "v0.16.0"
}
source: {
	kind: "self"
}
deps: {
	"cue.dev/x/githubactions@v0": {
		v:       "v0.3.0"
		default: true
	}
	"github.com/cue-lang/tmp/internal/ci@v0": {
		v:       "v0.0.17"
		default: true
	}
}
