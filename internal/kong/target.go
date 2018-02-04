package kong

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type TargetGetter interface {
	Target() TargetInterface
}

type TargetInterface interface {
	List(params url.Values, upstream string) (*TargetList, error)
	Get(name string) (*Target, *AdminResponse)
	Create(target *Target, upstream string) (*Target, *AdminResponse)
	Delete(name, upstream string) error
}

type targetAPI struct {
	client   rest.Interface
	resource *metav1.APIResource
}

func (a *targetAPI) Create(target *Target, upstream string) (*Target, *AdminResponse) {
	rawData, err := json.Marshal(target)
	if err != nil {
		return nil, &AdminResponse{err: err}
	}
	resp := a.client.Post().
		Resource("upstreams").
		SubResource(upstream, a.resource.Name).
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

	api := &Target{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *targetAPI) Get(name string) (*Target, *AdminResponse) {
	target := &Target{}
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

	response.err = json.Unmarshal(raw, target)
	return target, response
}

func (a *targetAPI) List(params url.Values, upstream string) (*TargetList, error) {
	targets := &TargetList{}
	request := a.client.Get().
		Resource("upstreams").
		SubResource(upstream, a.resource.Name)
	for k, vals := range params {
		for _, v := range vals {
			request.Param(k, v)
		}
	}
	data, err := request.DoRaw()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, targets); err != nil {
		return nil, err
	}

	if len(targets.NextPage) > 0 {
		params.Add("offset", targets.Offset)
		result, err := a.List(params, upstream)
		if err != nil {
			return nil, err
		}
		targets.Items = append(targets.Items, result.Items...)
	}

	return targets, err
}

func (a *targetAPI) Delete(name, upstream string) error {
	return a.client.Delete().
		RequestURI(fmt.Sprintf("/upstreams/%v/targets/%v", upstream, name)).
		Do().
		Error()
}
