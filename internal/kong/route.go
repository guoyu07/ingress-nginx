package kong

import (
	"encoding/json"
	"net/url"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

type RouteGetter interface {
	Route() RouteInterface
}

type RouteInterface interface {
	List(params url.Values) (*RouteList, error)
	Get(route string) (*Route, *AdminResponse)
	Create(route *Route) (*Route, *AdminResponse)
	Patch(route *Route) (*Route, *AdminResponse)
	Delete(route string) error
}

type routeAPI struct {
	client   rest.Interface
	resource *metav1.APIResource
}

func (a *routeAPI) Create(route *Route) (*Route, *AdminResponse) {
	rawData, err := json.Marshal(route)
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

	api := &Route{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *routeAPI) Patch(route *Route) (*Route, *AdminResponse) {
	rawData, err := json.Marshal(route)
	if err != nil {
		return nil, &AdminResponse{err: err}
	}
	resp := a.client.Patch(types.JSONPatchType).
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

	api := &Route{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *routeAPI) Get(name string) (*Route, *AdminResponse) {
	route := &Route{}
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

func (a *routeAPI) List(params url.Values) (*RouteList, error) {
	routeList := &RouteList{}
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

func (a *routeAPI) Delete(id string) error {
	return a.client.Delete().
		Resource(a.resource.Name).
		Name(id).
		Do().
		Error()
}
