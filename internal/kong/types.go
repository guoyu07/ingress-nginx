package kong

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AdminResponse struct {
	err        error
	StatusCode int
	Raw        []byte
}

func (r *AdminResponse) Error() error {
	return r.err
}

func (r *AdminResponse) String() string {
	if r.Raw == nil && r.StatusCode == 0 {
		return r.err.Error()
	}
	if r.Raw != nil {
		return fmt.Sprintf("[%d] %s", r.StatusCode, string(r.Raw))
	}
	return fmt.Sprintf("[%d] %s", r.StatusCode, r.err)
}

// Required defines the required fields to work between Kubernetes
// and Kong and also defines common field present in Kong entities
type Required struct {
	metav1.TypeMeta   `json:"-"`
	metav1.ObjectMeta `json:"-"`

	ID string `json:"id,omitempty"`

	Tags []string `json:"tags,omitempty"`

	CreatedAt int `json:"created_at,omitempty"`
	UpdatedAt int `json:"updated_at,omitempty"`
}

type RequiredList struct {
	metav1.TypeMeta `json:"-"`
	metav1.ListMeta `json:"-"`

	NextPage string `json:"next"`
	Offset   string `json:"offset"`
}

type SNI struct {
	Required `json:",inline"`

	Name        string `json:"name"`
	Certificate string `json:"ssl_certificate_id"`
}

type SNIList struct {
	RequiredList `json:",inline"`

	Items []SNI `json:"data"`
}

type Certificate struct {
	Required `json:",inline"`

	Cert  string   `json:"cert"`
	Key   string   `json:"key"`
	Hosts []string `json:"snis"`
}

type CertificateList struct {
	RequiredList `json:",inline"`

	Items []Certificate `json:"data"`
}

type Service struct {
	Required `json:",inline"`

	Name string `json:"name"`

	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Path     string `json:"path,omitempty"`

	Retries int `json:"retries"`

	ConnectTimeout int `json:"connect_timeout"`
	ReadTimeout    int `json:"read_timeout"`
	WriteTimeout   int `json:"write_timeout"`
}

type Upstream struct {
	Required `json:",inline"`

	Name string `json:"name"`
}

type Target struct {
	Required `json:",inline"`

	Target   string `json:"target"`
	Weight   int    `json:"weight,omitempty"`
	Upstream string `json:"upstream_id"`
}

type Route struct {
	Required `json:",inline"`

	Protocols []string `json:"protocols"`
	Hosts     []string `json:"hosts"`
	Paths     []string `json:"paths"`
	Methods   []string `json:"methods"`

	PreserveHost bool `json:"preserve_host"`
	StripPath    bool `json:"strip_path"`

	Service InlineService `json:"service"`
}

type InlineService struct {
	ID string `json:"id"`
}

type RouteList struct {
	RequiredList `json:",inline"`

	Items []Route `json:"data"`
}

type UpstreamList struct {
	RequiredList `json:",inline"`

	Items []Upstream `json:"data"`
}

type TargetList struct {
	RequiredList `json:",inline"`

	Items []Target `json:"data"`
}

// Equal tests for equality between two Configuration types
func (r1 *Route) Equal(r2 *Route) bool {
	if r1 == r2 {
		return true
	}
	if r1 == nil || r2 == nil {
		return false
	}

	if r1.Service.ID != r2.Service.ID {
		return false
	}

	if len(r1.Hosts) != len(r2.Hosts) {
		return false
	}

	for _, r1b := range r1.Hosts {
		found := false
		for _, r2b := range r2.Hosts {
			if r1b == r2b {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if len(r1.Paths) != len(r2.Paths) {
		return false
	}

	for _, r1b := range r1.Paths {
		found := false
		for _, r2b := range r2.Paths {
			if r1b == r2b {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
