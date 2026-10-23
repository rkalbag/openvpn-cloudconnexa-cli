package api

// Hand-written subsets of the v1.json schemas ccx uses. Every field in
// v1.json is optional (no `required` lists), so callers validate presence.

// Region is VpnRegionResponse.
type Region struct {
	ID         string `json:"id"`
	Continent  string `json:"continent"`
	Country    string `json:"country"`
	CountryISO string `json:"countryIso"`
	RegionName string `json:"regionName"`
}

// Internet access modes for HostCreateRequest.internetAccess.
const (
	InternetSplitTunnelOn  = "SPLIT_TUNNEL_ON"
	InternetSplitTunnelOff = "SPLIT_TUNNEL_OFF"
	InternetRestricted     = "RESTRICTED_INTERNET"
)

// Topology values for GET /settings/wpc/topology.
const (
	TopologyFullMesh = "FULL_MESH"
	TopologyCustom   = "CUSTOM"
)

// Connector connection statuses.
const (
	StatusOnline  = "ONLINE"
	StatusOffline = "OFFLINE"
)

// HostCreateRequest is the body of POST /hosts.
type HostCreateRequest struct {
	Name           string                 `json:"name"`
	Description    string                 `json:"description,omitempty"`
	InternetAccess string                 `json:"internetAccess,omitempty"`
	Domain         string                 `json:"domain,omitempty"`
	Connectors     []HostConnectorRequest `json:"connectors"`
	GatewaysIDs    []string               `json:"gatewaysIds,omitempty"`
}

// HostConnectorRequest is one connector in HostCreateRequest.
type HostConnectorRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	VPNRegionID string `json:"vpnRegionId"`
}

// Host is HostResponse.
type Host struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Description    string      `json:"description"`
	InternetAccess string      `json:"internetAccess"`
	Domain         string      `json:"domain"`
	Connectors     []Connector `json:"connectors"`
	SystemSubnets  []string    `json:"systemSubnets"`
	GatewaysIDs    []string    `json:"gatewaysIds"`
}

// Connector is HostConnectorResponse.
type Connector struct {
	ID               string `json:"id"`
	NetworkItemID    string `json:"networkItemId"`
	NetworkItemType  string `json:"networkItemType"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	VPNRegionID      string `json:"vpnRegionId"`
	IPv4Address      string `json:"ipV4Address"`
	IPv6Address      string `json:"ipV6Address"`
	ConnectionStatus string `json:"connectionStatus"`
	Licensed         bool   `json:"licensed"`
}

// IP service types (ServiceConfigRequest.serviceTypes).
const (
	ServiceAny    = "ANY"
	ServiceCustom = "CUSTOM"
)

// NamedServiceTypes lists the predefined serviceTypes values from v1.json
// other than ANY and CUSTOM.
var NamedServiceTypes = []string{
	"BGP", "DHCP", "DNS", "FTP", "HTTP", "HTTPS", "IMAP", "IMAPS", "NTP", "POP3",
	"POP3S", "RDP", "SMTP", "SMTPS", "SNMP", "SSH", "TELNET", "TFTP",
}

// IPServiceRequest is HostIpServiceRequest.
type IPServiceRequest struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Config      ServiceConfig `json:"config"`
}

// ServiceConfig is ServiceConfigRequest / ServiceConfigResponse.
type ServiceConfig struct {
	ServiceTypes       []string            `json:"serviceTypes,omitempty"`
	CustomServiceTypes []CustomServiceType `json:"customServiceTypes,omitempty"`
}

// CustomServiceType is CustomServiceTypeRequest / Response.
type CustomServiceType struct {
	Protocol string         `json:"protocol"` // TCP, UDP or ICMP
	Port     []RangeInteger `json:"port,omitempty"`
	ICMPType []RangeInteger `json:"icmpType,omitempty"`
}

// RangeInteger is either a single Value or LowerValue..UpperValue.
type RangeInteger struct {
	Value      *int `json:"value,omitempty"`
	LowerValue *int `json:"lowerValue,omitempty"`
	UpperValue *int `json:"upperValue,omitempty"`
}

// IPService is HostIpServiceResponse.
type IPService struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Description     string        `json:"description"`
	NetworkItemID   string        `json:"networkItemId"`
	NetworkItemType string        `json:"networkItemType"`
	Config          ServiceConfig `json:"config"`
}

// page is the common shape of v1.json's Page* responses.
type page[T any] struct {
	Content          []T `json:"content"`
	TotalElements    int `json:"totalElements"`
	TotalPages       int `json:"totalPages"`
	NumberOfElements int `json:"numberOfElements"`
	Page             int `json:"page"`
	Size             int `json:"size"`
}
