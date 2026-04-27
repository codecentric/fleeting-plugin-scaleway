package scaleway

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync/atomic"

	"github.com/hashicorp/go-hclog"
	"github.com/scaleway/scaleway-sdk-go/api/applesilicon/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

// namePrefix is embedded in every server name so we can identify servers that
// belong to this instance group when listing.
const namePrefix = "fleeting-"

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

// InstanceGroup implements the fleeting provider.InstanceGroup interface for
// Scaleway Apple Silicon servers.
type InstanceGroup struct {
	// AccessKey is the Scaleway API access key (SCWXXXXXXXXXXXXXXXXX format).
	// Required together with SecretKey when using API key authentication.
	// Falls back to the SCW_ACCESS_KEY environment variable when empty.
	AccessKey string `json:"access_key,omitempty"`

	// SecretKey is the Scaleway API secret key (UUID format).
	// Falls back to the SCW_SECRET_KEY environment variable when empty.
	// When provided without AccessKey it is used as a JWT/session token.
	SecretKey string `json:"secret_key,omitempty"`

	// ProjectID is the Scaleway project in which servers are created.
	// Falls back to the SCW_DEFAULT_PROJECT_ID environment variable when empty.
	ProjectID string `json:"project_id,omitempty"`

	// Zone is the Scaleway availability zone, e.g. "fr-par-3".
	// Falls back to the SCW_DEFAULT_ZONE environment variable when empty.
	// Currently only "fr-par-3" supports Apple Silicon.
	Zone string `json:"zone,omitempty"`

	// ServerType is the Mac mini type to provision, e.g. "M2-M", "M2-L", "M1-M".
	ServerType string `json:"server_type"`

	// OsID is the optional OS UUID to install. When empty the default OS for
	// the chosen server type is used.
	OsID string `json:"os_id,omitempty"`

	// Name is a logical name for this instance group. It is used as a
	// prefix/tag to identify servers managed by this group.
	Name string `json:"name"`

	log             hclog.Logger
	api             *applesilicon.API
	zone            scw.Zone
	instanceCounter atomic.Int32

	settings provider.Settings
}

// Init validates configuration, creates the Scaleway API client and returns
// provider metadata.
func (g *InstanceGroup) Init(ctx context.Context, log hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	if g.ServerType == "" {
		return provider.ProviderInfo{}, fmt.Errorf("server_type is required")
	}
	if g.Name == "" {
		return provider.ProviderInfo{}, fmt.Errorf("name is required")
	}

	opts := []scw.ClientOption{
		scw.WithEnv(), // honour SCW_* env vars
		scw.WithUserAgent("fleeting-plugin-scaleway/" + Version.Version),
	}

	switch {
	case g.AccessKey != "" && g.SecretKey != "":
		// Full API key authentication (access key + secret key).
		opts = append(opts, scw.WithAuth(g.AccessKey, g.SecretKey))
	case g.SecretKey != "":
		// Secret key only — use as a session/JWT token (same as X-Auth-Token header).
		opts = append(opts, scw.WithJWT(g.SecretKey))
	}
	if g.ProjectID != "" {
		opts = append(opts, scw.WithDefaultProjectID(g.ProjectID))
	}

	var zone scw.Zone
	if g.Zone != "" {
		zone = scw.Zone(g.Zone)
	} else {
		zone = scw.ZoneFrPar3 // only zone currently available for Apple Silicon
	}
	opts = append(opts, scw.WithDefaultZone(zone))

	client, err := scw.NewClient(opts...)
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("failed to create Scaleway client: %w", err)
	}

	g.api = applesilicon.NewAPI(client)
	g.zone = zone
	g.log = log.With("name", g.Name, "zone", zone)
	g.settings = settings

	// Validate that the zone is reachable and server type exists.
	if _, err := g.api.GetServerType(&applesilicon.GetServerTypeRequest{
		Zone:       g.zone,
		ServerType: g.ServerType,
	}); err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("failed to validate server type %q in zone %s: %w", g.ServerType, g.zone, err)
	}

	g.log.Info("Scaleway Apple Silicon plugin initialised", "server_type", g.ServerType)

	return provider.ProviderInfo{
		ID:        path.Join("scaleway", string(g.zone), g.Name),
		MaxSize:   10, // Apple Silicon quotas are typically small
		Version:   Version.Version,
		BuildInfo: Version.BuildInfo(),
	}, nil
}

// Update iterates over all servers in the project, filters to those belonging
// to this instance group, and reports their state to the fleeting runtime.
func (g *InstanceGroup) Update(ctx context.Context, fn func(instance string, state provider.State)) error {
	servers, err := g.listGroupServers(ctx)
	if err != nil {
		return err
	}

	for _, srv := range servers {
		state := serverState(srv)
		g.log.Debug("Server state", "id", srv.ID, "status", srv.Status, "state", state)
		fn(srv.ID, state)
	}

	return nil
}

// Increase provisions delta new Apple Silicon servers.
func (g *InstanceGroup) Increase(ctx context.Context, delta int) (int, error) {
	succeeded := 0
	var errs error

	for range delta {
		id, err := g.createServer(ctx)
		if err != nil {
			g.log.Error("Failed to create server", "err", err)
			errs = errors.Join(errs, err)
		} else {
			g.log.Info("Server creation requested", "id", id)
			succeeded++
		}
	}

	g.log.Info("Increase", "delta", delta, "succeeded", succeeded)
	return succeeded, errs
}

// Decrease deletes the specified servers.
func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) ([]string, error) {
	if len(instances) == 0 {
		return nil, nil
	}

	succeeded := make([]string, 0, len(instances))
	var errs error

	for _, id := range instances {
		err := g.api.DeleteServer(&applesilicon.DeleteServerRequest{
			Zone:     g.zone,
			ServerID: id,
		})
		if err != nil {
			// Scaleway Apple Silicon servers have a 24h minimum commitment
			// period. Treat this as a non-fatal condition: log a warning and
			// skip the instance rather than returning an error that causes
			// taskscaler to enter a retry loop.
			var precondition scw.PreconditionFailedError
			if errors.As(err, &precondition) {
				g.log.Warn("Server cannot be deleted yet (24h commitment)", "id", id, "err", err)
				continue
			}
			g.log.Error("Failed to delete server", "id", id, "err", err)
			errs = errors.Join(errs, err)
		} else {
			g.log.Info("Server deletion requested", "id", id)
			succeeded = append(succeeded, id)
		}
	}

	g.log.Info("Decrease", "requested", instances, "succeeded", succeeded)
	return succeeded, errs
}

// ConnectInfo returns the SSH connection details for a provisioned server.
func (g *InstanceGroup) ConnectInfo(ctx context.Context, instanceID string) (provider.ConnectInfo, error) {
	srv, err := g.api.GetServer(&applesilicon.GetServerRequest{
		Zone:     g.zone,
		ServerID: instanceID,
	})
	if err != nil {
		return provider.ConnectInfo{}, fmt.Errorf("failed to get server %s: %w", instanceID, err)
	}

	if srv.Status != applesilicon.ServerStatusReady {
		return provider.ConnectInfo{}, fmt.Errorf("server %s is not ready (status: %s)", instanceID, srv.Status)
	}

	if srv.IP == nil {
		return provider.ConnectInfo{}, fmt.Errorf("server %s has no IP address yet", instanceID)
	}

	ipAddr := srv.IP.String()

	connCfg := g.settings.ConnectorConfig
	// Apply sensible defaults for Apple Silicon if not already set by the user.
	if connCfg.OS == "" {
		connCfg.OS = "darwin"
	}
	if connCfg.Arch == "" {
		connCfg.Arch = "arm64"
	}
	if connCfg.Protocol == "" {
		connCfg.Protocol = provider.ProtocolSSH
	}
	// Scaleway provides the SSH username in the API response; use it if the
	// connector_config did not already specify one.
	if connCfg.Username == "" && srv.SSHUsername != "" {
		connCfg.Username = srv.SSHUsername
	}
	// Expose the sudo password as the SSH password so the runner can use it
	// for password-based authentication and for instance_ready_command.
	if connCfg.Password == "" && srv.SudoPassword != "" {
		connCfg.Password = srv.SudoPassword
	}

	return provider.ConnectInfo{
		ConnectorConfig: connCfg,
		ID:              instanceID,
		InternalAddr:    ipAddr,
		ExternalAddr:    ipAddr,
	}, nil
}

// Heartbeat checks whether a server still exists and is not in a terminal
// error state.
func (g *InstanceGroup) Heartbeat(ctx context.Context, instanceID string) error {
	srv, err := g.api.GetServer(&applesilicon.GetServerRequest{
		Zone:     g.zone,
		ServerID: instanceID,
	})
	if err != nil {
		return fmt.Errorf("heartbeat: failed to get server %s: %w", instanceID, err)
	}

	if srv.Status == applesilicon.ServerStatusError || srv.Status == applesilicon.ServerStatusLocked {
		return fmt.Errorf("%w: server %s is in status %s", provider.ErrInstanceUnhealthy, instanceID, srv.Status)
	}

	return nil
}

// Shutdown is a no-op; no persistent resources need to be cleaned up.
func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	return nil
}

// --- helpers -----------------------------------------------------------------

func (g *InstanceGroup) serverNamePrefix() string {
	return namePrefix + g.Name + "-"
}

func (g *InstanceGroup) createServer(ctx context.Context) (string, error) {
	index := int(g.instanceCounter.Add(1))
	name := fmt.Sprintf("%s%d", g.serverNamePrefix(), index)

	req := &applesilicon.CreateServerRequest{
		Zone:      g.zone,
		Name:      name,
		ProjectID: g.ProjectID,
		Type:      g.ServerType,
	}

	if g.OsID != "" {
		req.OsID = &g.OsID
	}

	srv, err := g.api.CreateServer(req)
	if err != nil {
		return "", fmt.Errorf("create server %q: %w", name, err)
	}

	return srv.ID, nil
}

func (g *InstanceGroup) listGroupServers(ctx context.Context) ([]*applesilicon.Server, error) {
	resp, err := g.api.ListServers(&applesilicon.ListServersRequest{
		Zone:      g.zone,
		ProjectID: &g.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}

	prefix := g.serverNamePrefix()
	filtered := make([]*applesilicon.Server, 0, len(resp.Servers))
	for _, srv := range resp.Servers {
		if strings.HasPrefix(srv.Name, prefix) {
			filtered = append(filtered, srv)
		}
	}

	return filtered, nil
}

// serverState maps a Scaleway ServerStatus to a fleeting provider.State.
func serverState(srv *applesilicon.Server) provider.State {
	switch srv.Status {
	case applesilicon.ServerStatusReady:
		return provider.StateRunning

	case applesilicon.ServerStatusStarting,
		applesilicon.ServerStatusRebooting,
		applesilicon.ServerStatusReinstalling,
		applesilicon.ServerStatusUpdating,
		applesilicon.ServerStatusLocking,
		applesilicon.ServerStatusUnlocking,
		applesilicon.ServerStatusUnknownStatus:
		return provider.StateCreating

	case applesilicon.ServerStatusError,
		applesilicon.ServerStatusLocked:
		return provider.StateDeleting

	default:
		return provider.StateCreating
	}
}


