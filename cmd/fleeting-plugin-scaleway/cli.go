package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	scaleway "github.com/codecentric/fleeting-plugin-scaleway"
	"github.com/scaleway/scaleway-sdk-go/api/applesilicon/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

const cliUsage = `Usage: fleeting-plugin-scaleway <command> [flags]

Commands:
  list          List servers in the instance group
  create        Create a new server in the instance group
  delete        Delete one or more servers by ID
  connect-info  Print SSH connection info for a server
  update        Reconcile and print current state of all group servers
  bootstrap     Install Homebrew, Tart and the nesting daemon on a server

Global flags (all commands):
  --name          Instance group name [required]
  --server-type   Server type, e.g. M2-M [required for create]
  --zone          Availability zone (required if SCW_DEFAULT_ZONE is not set)
  --project-id    Scaleway project ID (or SCW_DEFAULT_PROJECT_ID)
  --json          Output as JSON

Credentials are read from SCW_ACCESS_KEY and SCW_SECRET_KEY environment variables.
`

// commonFlags holds flags shared across all subcommands.
type commonFlags struct {
	name       string
	serverType string
	zone       string
	projectID  string
	jsonOut    bool
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.name, "name", os.Getenv("SCW_GROUP_NAME"), "Instance group name")
	fs.StringVar(&c.serverType, "server-type", "", "Server type (e.g. M2-M)")
	fs.StringVar(&c.zone, "zone", "", "Availability zone (default: fr-par-3)")
	fs.StringVar(&c.projectID, "project-id", "", "Scaleway project ID")
	fs.BoolVar(&c.jsonOut, "json", false, "Output as JSON")
}

func runCLI(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(cliUsage)
		os.Exit(0)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "list":
		cmdList(rest)
	case "create":
		cmdCreate(rest)
	case "delete":
		cmdDelete(rest)
	case "connect-info":
		cmdConnectInfo(rest)
	case "update":
		cmdUpdate(rest)
	case "bootstrap":
		cmdBootstrap(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, cliUsage)
		os.Exit(1)
	}
}

// --- list --------------------------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)

	g, api, zone := mustInitGroup(&cf, false)

	resp, err := api.ListServers(&applesilicon.ListServersRequest{
		Zone:      zone,
		ProjectID: projectIDPtr(cf.projectID),
	})
	dieOnErr(err, "listing servers")

	prefix := "fleeting-" + g.Name + "-"
	var servers []*applesilicon.Server
	for _, s := range resp.Servers {
		if strings.HasPrefix(s.Name, prefix) {
			servers = append(servers, s)
		}
	}

	if cf.jsonOut {
		printJSON(servers)
		return
	}

	if len(servers) == 0 {
		fmt.Println("No servers found in group", g.Name)
		return
	}

	fmt.Printf("%-38s  %-20s  %-14s  %s\n", "ID", "NAME", "STATUS", "IP")
	fmt.Println(strings.Repeat("-", 90))
	for _, s := range servers {
		ip := ""
		if s.IP != nil {
			ip = s.IP.String()
		}
		fmt.Printf("%-38s  %-20s  %-14s  %s\n", s.ID, s.Name, s.Status, ip)
	}
}

// --- create ------------------------------------------------------------------

func cmdCreate(args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	var osID string
	fs.StringVar(&osID, "os-id", "", "OS UUID to install (optional)")
	_ = fs.Parse(args)

	g, api, zone := mustInitGroup(&cf, true)

	req := &applesilicon.CreateServerRequest{
		Zone:      zone,
		Name:      fmt.Sprintf("fleeting-%s-1", g.Name),
		ProjectID: cf.projectID,
		Type:      cf.serverType,
	}
	if osID != "" {
		req.OsID = &osID
	}

	srv, err := api.CreateServer(req)
	dieOnErr(err, "creating server")

	if cf.jsonOut {
		printJSON(srv)
		return
	}
	fmt.Printf("Created server %s (%s) — status: %s\n", srv.ID, srv.Name, srv.Status)
}

// --- delete ------------------------------------------------------------------

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)

	ids := fs.Args()
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "delete: at least one server ID required")
		os.Exit(1)
	}

	_, api, zone := mustInitGroup(&cf, false)

	type result struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
		Error   string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(ids))

	for _, id := range ids {
		err := api.DeleteServer(&applesilicon.DeleteServerRequest{
			Zone:     zone,
			ServerID: id,
		})
		r := result{ID: id, Deleted: err == nil}
		if err != nil {
			r.Error = err.Error()
		}
		results = append(results, r)
	}

	if cf.jsonOut {
		printJSON(results)
		return
	}
	for _, r := range results {
		if r.Deleted {
			fmt.Printf("Deleted %s\n", r.ID)
		} else {
			fmt.Fprintf(os.Stderr, "Failed to delete %s: %s\n", r.ID, r.Error)
		}
	}
}

// --- connect-info ------------------------------------------------------------

func cmdConnectInfo(args []string) {
	fs := flag.NewFlagSet("connect-info", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)

	ids := fs.Args()
	if len(ids) != 1 {
		fmt.Fprintln(os.Stderr, "connect-info: exactly one server ID required")
		os.Exit(1)
	}

	_, api, zone := mustInitGroup(&cf, false)

	srv, err := api.GetServer(&applesilicon.GetServerRequest{
		Zone:     zone,
		ServerID: ids[0],
	})
	dieOnErr(err, "getting server")

	type info struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		IP          string `json:"ip"`
		SSHUsername string `json:"ssh_username"`
		OS          string `json:"os,omitempty"`
		Arch        string `json:"arch"`
		Protocol    string `json:"protocol"`
	}

	ip := ""
	if srv.IP != nil {
		ip = srv.IP.String()
	}
	osName := ""
	if srv.Os != nil {
		osName = srv.Os.Name
	}

	out := info{
		ID:          srv.ID,
		Name:        srv.Name,
		Status:      string(srv.Status),
		IP:          ip,
		SSHUsername: srv.SSHUsername,
		OS:          osName,
		Arch:        "arm64",
		Protocol:    "ssh",
	}

	if cf.jsonOut {
		printJSON(out)
		return
	}

	fmt.Printf("ID:           %s\n", out.ID)
	fmt.Printf("Name:         %s\n", out.Name)
	fmt.Printf("Status:       %s\n", out.Status)
	fmt.Printf("IP:           %s\n", out.IP)
	fmt.Printf("SSH user:     %s\n", out.SSHUsername)
	fmt.Printf("OS:           %s\n", out.OS)
	fmt.Printf("Arch:         %s\n", out.Arch)
	fmt.Printf("Protocol:     %s\n", out.Protocol)
	if ip != "" && out.SSHUsername != "" {
		fmt.Printf("Connect:      ssh %s@%s\n", out.SSHUsername, out.IP)
	}
}

// --- update ------------------------------------------------------------------

func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)

	g, api, zone := mustInitGroup(&cf, false)

	resp, err := api.ListServers(&applesilicon.ListServersRequest{
		Zone:      zone,
		ProjectID: projectIDPtr(cf.projectID),
	})
	dieOnErr(err, "listing servers")

	prefix := "fleeting-" + g.Name + "-"

	type entry struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		State  string `json:"fleeting_state"`
		IP     string `json:"ip,omitempty"`
	}
	var entries []entry

	for _, s := range resp.Servers {
		if !strings.HasPrefix(s.Name, prefix) {
			continue
		}
		ip := ""
		if s.IP != nil {
			ip = s.IP.String()
		}
		entries = append(entries, entry{
			ID:     s.ID,
			Name:   s.Name,
			Status: string(s.Status),
			State:  fleetingState(s.Status),
			IP:     ip,
		})
	}

	if cf.jsonOut {
		printJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("No servers found in group", g.Name)
		return
	}

	fmt.Printf("%-38s  %-20s  %-14s  %-12s  %s\n", "ID", "NAME", "STATUS", "STATE", "IP")
	fmt.Println(strings.Repeat("-", 100))
	for _, e := range entries {
		fmt.Printf("%-38s  %-20s  %-14s  %-12s  %s\n", e.ID, e.Name, e.Status, e.State, e.IP)
	}
}

// --- helpers -----------------------------------------------------------------

// mustInitGroup builds a minimal InstanceGroup and a direct API client.
// requireServerType causes a fatal error when --server-type is missing.
func mustInitGroup(cf *commonFlags, requireServerType bool) (*scaleway.InstanceGroup, *applesilicon.API, scw.Zone) {
	if cf.name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required (or set SCW_GROUP_NAME)")
		os.Exit(1)
	}
	if requireServerType && cf.serverType == "" {
		fmt.Fprintln(os.Stderr, "error: --server-type is required")
		os.Exit(1)
	}

	opts := []scw.ClientOption{scw.WithEnv()}
	if cf.projectID != "" {
		opts = append(opts, scw.WithDefaultProjectID(cf.projectID))
	}

	// Resolve zone: flag > SCW_DEFAULT_ZONE env var (honoured by WithEnv) > error.
	// We do NOT silently default to fr-par-3 so the user stays in control.
	var zone scw.Zone
	if cf.zone != "" {
		zone = scw.Zone(cf.zone)
		opts = append(opts, scw.WithDefaultZone(zone))
	} else if z := os.Getenv("SCW_DEFAULT_ZONE"); z != "" {
		zone = scw.Zone(z)
		// already picked up by WithEnv, but we need the value locally too
	} else {
		fmt.Fprintln(os.Stderr, "error: zone is required — use --zone or set SCW_DEFAULT_ZONE")
		os.Exit(1)
	}

	client, err := scw.NewClient(opts...)
	dieOnErr(err, "creating Scaleway client")

	g := &scaleway.InstanceGroup{
		Name:       cf.name,
		ServerType: cf.serverType,
		ProjectID:  cf.projectID,
		Zone:       string(zone),
	}

	return g, applesilicon.NewAPI(client), zone
}

func projectIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func fleetingState(status applesilicon.ServerStatus) string {
	switch status {
	case applesilicon.ServerStatusReady:
		return "running"
	case applesilicon.ServerStatusError, applesilicon.ServerStatusLocked:
		return "deleting"
	default:
		return "creating"
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "json encode error:", err)
		os.Exit(1)
	}
}

func dieOnErr(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error %s: %v\n", context, err)
		os.Exit(1)
	}
}
