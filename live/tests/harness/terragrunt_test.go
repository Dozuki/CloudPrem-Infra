package harness

import (
	"strings"
	"testing"
)

func TestTGEnv(t *testing.T) {
	opt := TGOptions{
		AccountID:    "076000000000",
		Region:       "us-east-1",
		Profile:      "ddvtest",
		BucketPrefix: "run123-",
		StatePrefix:  "run123-min/",
	}
	env := opt.env()
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"TG_AWS_ACCT_ID=076000000000",
		"TG_AWS_REGION=us-east-1",
		"TG_AWS_PROFILE=ddvtest",
		"TG_BUCKET_PREFIX=run123-",
		"TG_STATE_PREFIX=run123-min/",
		"TG_NON_INTERACTIVE=true",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q", want)
		}
	}
}
