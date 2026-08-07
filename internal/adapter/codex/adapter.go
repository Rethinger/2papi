package codex

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
)

const Name = "openai-codex"

const (
	productionAuthBaseURL    = "https://auth.openai.com"
	productionBackendBaseURL = "https://chatgpt.com"
	defaultClientVersion     = "codex-gateway/1"
)

var (
	ErrSnapshotRefreshRequired       = errors.New("codex snapshot refresh required")
	ErrCredentialPersistenceDegraded = errors.New("codex credential persistence degraded")
)

type CredentialPersistResult struct {
	Revision int64
	Digest   string
}

type CredentialSink interface {
	Persist(context.Context, string, int64, config.Credential) (CredentialPersistResult, error)
}

type SnapshotRefreshTrigger interface {
	TriggerSnapshotRefresh(reason string)
}

type Options struct {
	TestMode       bool
	AuthBaseURL    string
	BackendBaseURL string
	ClientVersion  string
	Now            func() time.Time
}

type Adapter struct {
	client *http.Client
	auth   *tokenManager
	models *modelClient
}

func New(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, options Options) *Adapter {
	if client == nil {
		client = http.DefaultClient
	}
	options = normalizeOptions(options)
	return &Adapter{client: client, auth: newTokenManager(client, sink, refresh, options), models: newModelClient(client, options)}
}

func Register(reg *adapter.Registry, client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, options Options) error {
	return reg.Register(Name, New(client, sink, refresh, options))
}

func normalizeOptions(o Options) Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ClientVersion == "" {
		o.ClientVersion = defaultClientVersion
	}
	if os.Getenv("CODEX_TEST_MODE") == "1" || os.Getenv("CODEX_TEST_MODE") == "true" {
		o.TestMode = true
	}
	if o.AuthBaseURL == "" {
		o.AuthBaseURL = productionAuthBaseURL
	}
	if o.BackendBaseURL == "" {
		o.BackendBaseURL = productionBackendBaseURL
	}
	if !o.TestMode {
		o.AuthBaseURL = productionAuthBaseURL
		o.BackendBaseURL = productionBackendBaseURL
	}
	return o
}

func (a *Adapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
}

func (a *Adapter) Operate(ctx context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	switch op.Kind {
	case adapter.OperationDiscoverModels:
		cred, rev, warning, err := a.auth.accessToken(ctx, op.Account, false)
		if err != nil {
			return adapter.OperationResult{}, err
		}
		data, err := a.models.discover(ctx, cred)
		if isUnauthorized(err) {
			cred, rev, warning, err = a.auth.accessToken(ctx, op.Account, true)
			if err != nil {
				return adapter.OperationResult{}, err
			}
			data, err = a.models.discover(ctx, cred)
			if isUnauthorized(err) {
				return adapter.OperationResult{}, errors.New("codex discovery unauthorized after refresh")
			}
		}
		if err != nil {
			return adapter.OperationResult{}, err
		}
		return adapter.OperationResult{Data: data, CredentialRevision: rev, WarningCode: warning}, nil
	case adapter.OperationValidateCredentials:
		cred, rev, warning, err := a.auth.accessToken(ctx, op.Account, false)
		if err != nil {
			return adapter.OperationResult{}, err
		}
		err = a.models.validate(ctx, cred)
		if isUnauthorized(err) {
			cred, rev, warning, err = a.auth.accessToken(ctx, op.Account, true)
			if err != nil {
				return adapter.OperationResult{}, err
			}
			err = a.models.validate(ctx, cred)
			if isUnauthorized(err) {
				return adapter.OperationResult{}, errors.New("codex validation unauthorized after refresh")
			}
		}
		if err != nil {
			return adapter.OperationResult{}, err
		}
		return adapter.OperationResult{Data: []byte(`{"valid":true}`), CredentialRevision: rev, WarningCode: warning}, nil
	default:
		return adapter.OperationResult{}, &adapter.CapabilityError{Kind: op.Kind}
	}
}

type ControlPlaneSink struct{ Client *controlplane.Client }

func (s ControlPlaneSink) Persist(ctx context.Context, accountID string, expectedRevision int64, credential config.Credential) (CredentialPersistResult, error) {
	if s.Client == nil {
		return CredentialPersistResult{}, errors.New("control-plane credential sink unavailable")
	}
	res, err := s.Client.UpdateCredentials(ctx, accountID, expectedRevision, credential)
	if err != nil {
		return CredentialPersistResult{}, err
	}
	return CredentialPersistResult{Revision: res.CredentialRevision, Digest: res.CredentialDigest}, nil
}
