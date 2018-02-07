package kong

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/blang/semver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type KongInterface interface {
	RESTClient() rest.Interface

	RouteGetter
	ServiceGetter
	UpstreamGetter
	TargetGetter
	SNIGetter
	CertificateGetter
}

type RestClient struct {
	restClient rest.Interface
}

func (c *RestClient) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}

func (c *RestClient) Routes() RouteInterface {
	return &routeAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "routes",
			Namespaced: false,
		},
	}
}

func (c *RestClient) Services() ServiceInterface {
	return &serviceAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "services",
			Namespaced: false,
		},
	}
}

func (c *RestClient) Upstream() UpstreamInterface {
	return &upstreamAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "upstreams",
			Namespaced: false,
		},
	}
}

func (c *RestClient) Target() TargetInterface {
	return &targetAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "targets",
			Namespaced: false,
		},
	}
}

func (c *RestClient) SNI() SNIInterface {
	return &sniAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "snis",
			Namespaced: false,
		},
	}
}

func (c *RestClient) Certificate() CertificateInterface {
	return &certificateAPI{
		client: c.RESTClient(),
		resource: &metav1.APIResource{
			Name:       "certificates",
			Namespaced: false,
		},
	}
}

func (c *RestClient) GetVersion() (semver.Version, error) {
	var info map[string]interface{}
	data, err := c.RESTClient().Get().RequestURI("/").DoRaw()
	if err != nil {
		return semver.Version{}, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return semver.Version{}, err
	}

	if version, ok := info["version"]; ok {
		v := version.(string)
		// fix bad version formats like 0.13.0preview1
		re := regexp.MustCompile(`(.*\d)(preview.*)`)
		if re.MatchString(v) {
			v = re.ReplaceAllString(v, "$1-$2")
		}

		return semver.Make(v)
	}

	return semver.Version{}, fmt.Errorf("Unknown Kong version")
}

func NewRESTClient(c *rest.Config) (*RestClient, error) {
	c.ContentConfig = dynamic.ContentConfig()
	cl, err := rest.UnversionedRESTClientFor(c)
	if err != nil {
		return nil, err
	}
	return &RestClient{restClient: cl}, nil
}
