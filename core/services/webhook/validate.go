package webhook

import (
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"

	"github.com/smartcontractkit/chainlink/core/services/job"
	"github.com/smartcontractkit/chainlink/core/services/pipeline"
)

var ErrMissingJobID = errors.New("missing job ID")

func ValidateWebhookSpec(tomlString string) (job.Job, error) {
	var jb = job.Job{
		Pipeline: *pipeline.NewTaskDAG(),
	}
	tree, err := toml.Load(tomlString)
	if err != nil {
		return jb, err
	}
	err = tree.Unmarshal(&jb)
	if err != nil {
		return jb, err
	}
	if jb.ExternalJobID == (uuid.UUID{}) {
		return jb, ErrMissingJobID
	}
	if jb.Type != job.Webhook {
		return jb, errors.Errorf("unsupported type %s", jb.Type)
	}
	if jb.SchemaVersion != uint32(1) {
		return jb, errors.Errorf("the only supported schema version is currently 1, got %v", jb.SchemaVersion)
	}

	var spec job.WebhookSpec
	err = tree.Unmarshal(&spec)
	if err != nil {
		return jb, err
	}
	jb.WebhookSpec = &spec
	return jb, nil
}