package kong

import (
	"encoding/json"
	"net/url"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type SNIGetter interface {
	SNI() SNIInterface
}

type SNIInterface interface {
	List(params url.Values) (*SNIList, error)
	Get(name string) (*SNI, *AdminResponse)
	Create(sni *SNI) (*SNI, *AdminResponse)
	Delete(name string) error
}

type sniAPI struct {
	client   rest.Interface
	resource *metav1.APIResource
}

func (a *sniAPI) Create(target *SNI) (*SNI, *AdminResponse) {
	rawData, err := json.Marshal(target)
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

	api := &SNI{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *sniAPI) Get(name string) (*SNI, *AdminResponse) {
	sni := &SNI{}
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

	response.err = json.Unmarshal(raw, sni)
	return sni, response
}

func (a *sniAPI) List(params url.Values) (*SNIList, error) {
	targets := &SNIList{}
	request := a.client.Get().
		Resource(a.resource.Name)
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
		result, err := a.List(params)
		if err != nil {
			return nil, err
		}
		targets.Items = append(targets.Items, result.Items...)
	}

	return targets, err
}

func (a *sniAPI) Delete(name string) error {
	return a.client.Delete().
		Resource(a.resource.Name).
		Do().
		Error()
}
