package kong

import (
	"encoding/json"
	"net/url"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type UpstreamGetter interface {
	Upstream() UpstreamInterface
}

type UpstreamInterface interface {
	List(params url.Values) (*UpstreamList, error)
	Get(name string) (*Upstream, *AdminResponse)
	Create(route *Upstream) (*Upstream, *AdminResponse)
	Delete(name string) error
}

type upstreamAPI struct {
	client   rest.Interface
	resource *metav1.APIResource
}

func (a *upstreamAPI) Create(in *Upstream) (*Upstream, *AdminResponse) {
	rawData, err := json.Marshal(in)
	if err != nil {
		return nil, &AdminResponse{err: err}
	}
	resp := a.client.Post().
		Resource(a.resource.Name).
		Body(rawData).
		SetHeader("Content-Type", "application/json").
		Do()

	statusCode := reflect.ValueOf(resp).FieldByName("statusCode").Int()
	raw, err := resp.Raw()
	response := &AdminResponse{StatusCode: int(statusCode), err: err}

	if err != nil {
		response.Raw = raw
		return nil, response
	}

	api := &Upstream{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *upstreamAPI) Get(name string) (*Upstream, *AdminResponse) {
	route := &Upstream{}
	resp := a.client.Get().
		Resource(a.resource.Name).
		Name(name).
		Do()
	statusCode := reflect.ValueOf(resp).FieldByName("statusCode").Int()
	raw, err := resp.Raw()
	response := &AdminResponse{StatusCode: int(statusCode), err: err}
	if err != nil {
		response.Raw = raw
		return nil, response
	}

	response.err = json.Unmarshal(raw, route)
	return route, response
}

func (a *upstreamAPI) List(params url.Values) (*UpstreamList, error) {
	routeList := &UpstreamList{}
	request := a.client.Get().Resource(a.resource.Name)
	for k, vals := range params {
		for _, v := range vals {
			request.Param(k, v)
		}
	}
	data, err := request.DoRaw()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, routeList); err != nil {
		return nil, err
	}

	if len(routeList.NextPage) > 0 {
		params.Add("offset", routeList.Offset)
		result, err := a.List(params)
		if err != nil {
			return nil, err
		}
		routeList.Items = append(routeList.Items, result.Items...)
	}

	return routeList, err
}

func (a *upstreamAPI) Delete(id string) error {
	return a.client.Delete().
		Resource(a.resource.Name).
		Name(id).
		Do().
		Error()
}
