package agent

import (
	"context"
	"crypto/tls"
	"os"
	"runtime"

	"github.com/google/uuid"
	"github.com/honeycombio/refinery/config"
	"github.com/honeycombio/refinery/metrics"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

type Agent struct {
	agentType          string
	agentVersion       string
	instanceId         uuid.UUID
	effectiveConfig    config.Config
	agentDescription   *protobufs.AgentDescription
	opampClient        client.OpAMPClient
	remoteConfigStatus *protobufs.RemoteConfigStatus
	opampClientCert    *tls.Certificate
	caCertPath         string

	certRequested       bool
	clientPrivateKeyPEM []byte

	logger  Logger
	metrics metrics.Metrics
}

func NewAgent(refineryLogger Logger, agentType string, agentVersion string, currentConfig config.Config) *Agent {
	agent := &Agent{
		logger:          refineryLogger,
		agentType:       agentType,
		agentVersion:    agentVersion,
		effectiveConfig: currentConfig,
	}
	agent.createAgentIdentity()
	agent.logger.Debugf(context.Background(), "starting opamp client, id=%v", agent.instanceId)
	if err := agent.connect(); err != nil {
		agent.logger.Errorf(context.Background(), "Failed to connect to OpAMP Server: %v", err)
		return nil
	}
	return agent
}

func (agent *Agent) createAgentIdentity() {
	uid, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	agent.instanceId = uid
	hostname, _ := os.Hostname()
	agent.agentDescription = &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentType},
				},
			},
			{
				Key: "service.version",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentVersion},
				},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "os.type",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: runtime.GOOS},
				},
			},
			{
				Key: "host.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: hostname},
				},
			},
		},
	}
}

func (agent *Agent) connect() error {
	agent.opampClient = client.NewWebSocket(&agent.logger)

	settings := types.StartSettings{
		OpAMPServerURL: agent.effectiveConfig.GetOpAMPConfig().OpAMPServerURL,
		InstanceUid:    types.InstanceUid(agent.instanceId),
		Callbacks: types.CallbacksStruct{
			OnConnectFunc: func(ctx context.Context) {
				agent.logger.Debugf(ctx, "connected to OpAMP server")
			},
			OnConnectFailedFunc: func(ctx context.Context, err error) {
				agent.logger.Errorf(ctx, "Failed to connect to server: %v", err)
			},
			OnErrorFunc: func(ctx context.Context, err *protobufs.ServerErrorResponse) {
				agent.logger.Errorf(ctx, "Received error from server: %v", err)
			},
			SaveRemoteConfigStatusFunc: func(ctx context.Context, status *protobufs.RemoteConfigStatus) {
				agent.remoteConfigStatus = status
			},
			GetEffectiveConfigFunc: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
				return agent.composeEffectiveConfig(), nil
			},
			OnMessageFunc:                 agent.onMessage,
			OnOpampConnectionSettingsFunc: agent.onOpampConnectionSettings,
		},
		RemoteConfigStatus: agent.remoteConfigStatus,
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics |
			protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth,
	}

	err := agent.opampClient.SetAgentDescription(agent.agentDescription)
	if err != nil {
		return err
	}
	err = agent.opampClient.SetHealth(agent.getHealth())

	agent.logger.Debugf(context.Background(), "starting opamp client")

	err = agent.opampClient.Start(context.Background(), settings)
	if err != nil {
		return err
	}
	agent.logger.Debugf(context.Background(), "started opamp client")
	return nil
}

func (agent *Agent) disconnect(ctx context.Context) {
	agent.logger.Debugf(ctx, "disconnecting from OpAMP server")
	agent.opampClient.Stop(ctx)
}

func (agent *Agent) composeEffectiveConfig() *protobufs.EffectiveConfig {
	configYAML, err := config.SerializeToYAML(agent.effectiveConfig)
	if err != nil {
		agent.logger.Errorf(context.Background(), "Failed to marshal effective config: %v", err)
		return nil
	}
	return &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: []byte(configYAML)},
			},
		},
	}
}

func (agent *Agent) onMessage(ctx context.Context, msg *types.MessageData) {
	if msg.RemoteConfig != nil {
		agent.logger.Debugf(ctx, "got remote config")
	}
	if msg.OwnMetricsConnSettings != nil {
		agent.logger.Debugf(ctx, "got own metrics connection settings")
	}
	if msg.AgentIdentification != nil {
		agent.logger.Debugf(ctx, "got agent identification")
	}

}

func (agent *Agent) getHealth() *protobufs.ComponentHealth {
	return &protobufs.ComponentHealth{
		Healthy: true,
	}
}

func (agent *Agent) onOpampConnectionSettings(ctx context.Context, settings *protobufs.OpAMPConnectionSettings) error {
	agent.logger.Debugf(ctx, "got connection settings")
	return nil
}
