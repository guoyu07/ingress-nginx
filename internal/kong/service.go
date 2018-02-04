package kong

import (
	"encoding/json"
	"net/url"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type ServiceGetter interface {
	Service() ServiceInterface
}

type ServiceInterface interface {
	List(params url.Values) (*ServiceList, error)
	Get(name string) (*Service, *AdminResponse)
	Create(service *Service) (*Service, *AdminResponse)
	Delete(name string) error
}

type ServiceList struct {
	metav1.TypeMeta `json:"-"`
	metav1.ListMeta `json:"-"`

	Items    []Service `json:"data"`
	NextPage string    `json:"next"`
	Offset   string    `json:"offset"`
}

type serviceAPI struct {
	client   rest.Interface
	resource *metav1.APIResource
}

func (a *serviceAPI) Create(service *Service) (*Service, *AdminResponse) {
	rawData, err := json.Marshal(service)
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

	api := &Service{}
	response.err = json.Unmarshal(raw, api)
	return api, response
}

func (a *serviceAPI) Get(name string) (*Service, *AdminResponse) {
	service := &Service{}
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

	response.err = json.Unmarshal(raw, service)
	return service, response
}

func (a *serviceAPI) List(params url.Values) (*ServiceList, error) {
	ServiceList := &ServiceList{}
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
	if err := json.Unmarshal(data, ServiceList); err != nil {
		return nil, err
	}

	if len(ServiceList.NextPage) > 0 {
		params.Add("offset", ServiceList.Offset)
		result, err := a.List(params)
		if err != nil {
			return nil, err
		}
		ServiceList.Items = append(ServiceList.Items, result.Items...)
	}

	return ServiceList, err
}

func (a *serviceAPI) Delete(id string) error {
	return a.client.Delete().
		Resource(a.resource.Name).
		Name(id).
		Do().
		Error()
}
