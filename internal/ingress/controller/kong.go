/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/util/sets"

	"k8s.io/ingress-nginx/internal/ingress"
	"k8s.io/ingress-nginx/internal/kong"
)

// OnUpdate is called periodically by syncQueue to keep the configuration in sync.
//
// 1. converts configmap configuration to custom configuration object
// 2. write the custom template (the complexity depends on the implementation)
// 3. write the configuration file
//
// returning nill implies the backend will be reloaded.
// if an error is returned means requeue the update
func (n *NGINXController) OnUpdate(ingressCfg ingress.Configuration) error {
	// Synchronizde the state between Kubernetes and Kong with this order:
	//	- SSL Certificates
	//	- SNIs
	// 	- Upstreams
	//	- Upstream targets
	// 	- Services
	//  - Routes

	// All the resources created by the ingress controller add the annotations
	// kong-ingress-controller and kubernetes
	// This allows the identification of reources that can be removed if they
	// are not present in Kubernetes when the sync process is executed
	// For instance an Ingress, Service or Secret is removed.

	// Note: returning an error in the sync loop means retry.

	// TODO: find a way to avoid the cluncky logic to keep in sync the models between
	// kubernetes and Kong. This should not require so many requests.

	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			// there is no catch all server in kong
			continue
		}

		// check the certificate is present in kong
		if server.SSLCertificate != "" {
			err := syncCertificate(server, n.cfg.KongClient)
			if err != nil {
				return err
			}
		}

		err := syncUpstreams(server.Locations, ingressCfg.Backends, n.cfg.KongClient)
		if err != nil {
			return err
		}
	}

	err := syncServices(ingressCfg, n.cfg.KongClient)
	if err != nil {
		return err
	}

	err = syncRoutes(ingressCfg, n.cfg.KongClient)
	if err != nil {
		return err
	}

	return nil
}

// syncTargets reconciles the state between the ingress controller and
// kong comparing the endpoints in Kubernetes and the targets in a
// particular kong upstream. To avoid downtimes we create the new targets
// first and then remove the killed ones.
func syncTargets(upstream string, kongUpstreams []kong.Upstream,
	ingressEndpopint *ingress.Backend, client *kong.RestClient) error {
	b := getKongUpstream(upstream, kongUpstreams)
	if b == nil {
		glog.Errorf("there is no upstream for hostname %v", upstream)
		return nil
	}

	kongTargets, err := client.Target().List(nil, upstream)
	if err != nil {
		return err
	}

	oldTargets := sets.NewString()
	for _, kongTarget := range kongTargets.Items {
		if !oldTargets.Has(kongTarget.Target) {
			oldTargets.Insert(kongTarget.Target)
		}
	}

	newTargets := sets.NewString()
	for _, endpoint := range ingressEndpopint.Endpoints {
		nt := fmt.Sprintf("%v:%v", endpoint.Address, endpoint.Port)
		if !newTargets.Has(nt) {
			newTargets.Insert(nt)
		}
	}

	add := newTargets.Difference(oldTargets).List()
	remove := oldTargets.Difference(newTargets).List()

	for _, endpoint := range add {
		target := &kong.Target{
			Target:   endpoint,
			Upstream: b.ID,
		}
		glog.Infof("creating Kong Target %v for upstream %v", endpoint, b.ID)
		_, res := client.Target().Create(target, upstream)
		if res.StatusCode != 201 {
			glog.Errorf("Unexpected error creating Kong Upstream: %v", res.Error())
			return res.Error()
		}
	}

	// wait to avoid hitting the kong API server too fast
	time.Sleep(100 * time.Millisecond)

	for _, endpoint := range remove {
		for _, kongTarget := range kongTargets.Items {
			if kongTarget.Target != endpoint {
				continue
			}
			glog.Infof("deleting Kong Target %v from upstream %v", kongTarget.ID, kongTarget.Upstream)
			err := client.Target().Delete(kongTarget.ID, b.ID)
			if err != nil {
				glog.Errorf("Unexpected error deleting Kong Upstream: %v", err)
				return err
			}

		}
	}

	return nil
}

func syncServices(ingressCfg ingress.Configuration, client *kong.RestClient) error {
	kongServices, err := client.Services().List(nil)
	if err != nil {
		return err
	}

	// create a copy of the existing services to be able to run a comparison
	servicesToRemove := sets.NewString()
	for _, old := range kongServices.Items {
		if !servicesToRemove.Has(old.Name) {
			servicesToRemove.Insert(old.Name)
		}
	}

	// Check if the endpoints exists as a service in kong
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			// there is no catch all server in kong
			continue
		}

		for _, location := range server.Locations {
			backend := location.Backend
			if backend == "default-backend" {
				// there is no default backend in Kong
				continue
			}

			for _, upstream := range ingressCfg.Backends {
				if upstream.Name != backend {
					continue
				}

				proto := "http"
				port := 80
				if upstream.Secure {
					proto = "https"
					port = 443
				}

				name := buildName(backend, location)
				if !isServiceInKong(name, kongServices.Items) {
					s := &kong.Service{
						Name:           name,
						Path:           "/",
						Protocol:       proto,
						Host:           name,
						Port:           port,
						ConnectTimeout: location.Proxy.ConnectTimeout,
						ReadTimeout:    location.Proxy.ReadTimeout,
						WriteTimeout:   location.Proxy.SendTimeout,
						Retries:        5,
					}
					glog.Infof("creating Kong Service name %v", name)
					svc, res := client.Services().Create(s)
					if res.StatusCode != 201 {
						glog.Errorf("Unexpected error creating Kong Service: %v", res.Error())
						return res.Error()
					}

					kongServices.Items = append(kongServices.Items, *svc)
				} else {
					if servicesToRemove.Has(name) {
						servicesToRemove.Delete(name)
					}
				}

				break
			}
		}
	}

	// remove all those services that are present in Kong but not in the current Kubernetes state
	for _, svcName := range servicesToRemove.List() {
		svc := getKongService(svcName, kongServices.Items)
		if svc == nil {
			continue
		}

		glog.Infof("deleting Kong Service %v", svcName)
		// before deleting the service we need to remove the upstream and th targets that reference the service
		err := deleteServiceUpstream(svc.Name, client)
		if err != nil {
			glog.Errorf("Unexpected error deleting Kong ppstreams and targets that depend on service %v: %v", svc.Name, err)
			continue
		}
		err = client.Services().Delete(svc.ID)
		if err != nil {
			// this means the service is being referenced by a route
			// during the next sync it will be removed
			glog.V(3).Infof("Unexpected error deleting Kong Service: %v", err)
		}
	}

	return nil
}

func syncRoutes(ingressCfg ingress.Configuration, client *kong.RestClient) error {
	kongRoutes, err := client.Routes().List(nil)
	if err != nil {
		return err
	}

	// create a copy of the existing routes to be able to run a comparison
	routesToRemove := sets.NewString()
	for _, old := range kongRoutes.Items {
		if !routesToRemove.Has(old.ID) {
			routesToRemove.Insert(old.ID)
		}
	}

	kongServices, err := client.Services().List(nil)
	if err != nil {
		return err
	}

	// Routes
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			// there is no catch all server in kong
			continue
		}

		protos := []string{"http"}
		if server.SSLCertificate != "" {
			protos = append(protos, "https")
		}

		for _, location := range server.Locations {
			backend := location.Backend
			if backend == "default-backend" {
				// there is no default backend in Kong
				continue
			}

			name := buildName(backend, location)
			svc := getKongService(name, kongServices.Items)
			if svc == nil {
				glog.V(3).Infof("there is no Kong service with name %v", name)
				continue
			}

			r := &kong.Route{
				Paths:     []string{location.Path},
				Protocols: protos,
				Hosts:     []string{server.Hostname},
				Service:   kong.InlineService{ID: svc.ID},
			}

			if !isRouteInKong(r, kongRoutes.Items) {
				glog.Infof("creating Kong Route for host %v, path %v and service %v", server.Hostname, location.Path, svc.ID)
				r, res := client.Routes().Create(r)
				if res.StatusCode != 201 {
					glog.Errorf("Unexpected error creating Kong Route: %v", res.Error())
					return res.Error()
				}

				kongRoutes.Items = append(kongRoutes.Items, *r)
			} else {
				// the route could exists but the protocols could be different (we are adding ssl)
				route := getKongRoute(server.Hostname, location.Path, kongRoutes.Items)

				if routesToRemove.Has(route.ID) {
					routesToRemove.Delete(route.ID)
				}

				for _, proto := range protos {
					found := false
					for _, rp := range route.Protocols {
						if proto == rp {
							found = true
							break
						}
					}

					if !found {
						glog.Infof("updating Kong Route for host %v, path %v and service %v (change in protocols)", server.Hostname, location.Path, svc.ID)
						route.Protocols = protos
						_, res := client.Routes().Patch(route)
						if res.StatusCode != 201 {
							glog.Errorf("Unexpected error updating Kong Route: %v", res.Error())
							return res.Error()
						}
					}
				}
			}
		}
	}

	// remove all those routes that are present in Kong but not in the current Kubernetes state
	for _, route := range routesToRemove.List() {
		glog.Infof("deleting Kong Route %v", route)
		err := client.Routes().Delete(route)
		if err != nil {
			glog.Errorf("Unexpected error deleting Kong Route: %v", err)
		}
	}

	return nil
}

func syncUpstreams(locations []*ingress.Location, backends []*ingress.Backend, client *kong.RestClient) error {
	kongUpstreams, err := client.Upstream().List(nil)
	if err != nil {
		return err
	}

	for _, location := range locations {
		backend := location.Backend
		if backend == "default-backend" {
			// there is no default backend in Kong
			continue
		}

		for _, upstream := range backends {
			if upstream.Name != backend {
				continue
			}

			upstreamName := buildName(backend, location)

			if !isUpstreamInKong(upstreamName, kongUpstreams.Items) {
				upstream := &kong.Upstream{
					Name: upstreamName,
				}

				glog.Infof("creating Kong Upstream with name %v", upstreamName)

				u, res := client.Upstream().Create(upstream)
				if res.StatusCode != 201 {
					glog.Errorf("Unexpected error creating Kong Upstream: %v", res.Error())
					return res.Error()
				}

				// Add the new upstream to avoid new requests
				kongUpstreams.Items = append(kongUpstreams.Items, *u)
			}

			err := syncTargets(upstreamName, kongUpstreams.Items, upstream, client)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func syncCertificate(server *ingress.Server, client *kong.RestClient) error {
	kongCertificates, err := client.Certificate().List(nil)
	if err != nil {
		return err
	}

	if isCertificateInKong(server.Hostname, kongCertificates.Items) {
		// TODO: we need to compare certificates?
		return nil
	}

	sc := bytes.NewBuffer(server.SSLCert.Raw.Cert).String()
	sk := bytes.NewBuffer(server.SSLCert.Raw.Key).String()
	cert := &kong.Certificate{
		Cert:  sc,
		Key:   sk,
		Hosts: []string{server.Hostname},
	}

	glog.Infof("creating Kong SSL Certificate for host %v", server.Hostname)

	cert, res := client.Certificate().Create(cert)
	if res.StatusCode != 201 {
		glog.Errorf("Unexpected error creating Kong Certificate: %v", res.Error())
		return res.Error()
	}

	sni := &kong.SNI{
		Name:        server.Hostname,
		Certificate: cert.ID,
	}

	glog.Infof("creating Kong SNI for host %v and certificate id %v", server.Hostname, cert.ID)

	_, res = client.SNI().Create(sni)
	if res.StatusCode != 201 {
		glog.Errorf("Unexpected error creating Kong SNI: %v", res.Error())
		return res.Error()
	}

	return nil
}

// buildName returns a string valid as a hostnames taking a backend and
// location as input. The format of backend is <namespace>-<service name>-<port>
// For the location the field Path is used. If the path is / only the backend is used
// This process ensures the returned name is unique.
func buildName(backend string, location *ingress.Location) string {
	wd := strings.Replace(backend, "-", ".", -1)

	if location.Path == "/" {
		return wd
	}

	bn := []string{wd}
	ns := strings.TrimPrefix(location.Path, "/")
	ns = strings.Replace(ns, "/", ".", -1)

	bn = append(bn, ns)
	return strings.Join(bn, ".")
}

func getKongRoute(hostname, path string, routes []kong.Route) *kong.Route {
	for _, r := range routes {
		if sets.NewString(r.Paths...).Has(path) &&
			sets.NewString(r.Hosts...).Has(hostname) {
			return &r
		}
	}

	return nil
}

func getKongService(name string, services []kong.Service) *kong.Service {
	for _, svc := range services {
		if svc.Name == name {
			return &svc
		}
	}

	return nil
}

func getKongUpstream(name string, upstreams []kong.Upstream) *kong.Upstream {
	for _, upstream := range upstreams {
		if upstream.Name == name {
			return &upstream
		}
	}

	return nil
}

func getUpstreamTarget(target string, targets []kong.Target) *kong.Target {
	for _, ut := range targets {
		if ut.Target == target {
			return &ut
		}
	}

	return nil
}

func isTargetInKong(host, port string, targets []kong.Target) bool {
	for _, t := range targets {
		if t.Target == fmt.Sprintf("%v:%v", host, port) {
			return true
		}
	}

	return false
}

func isCertificateInKong(host string, certs []kong.Certificate) bool {
	for _, cert := range certs {
		s := sets.NewString(cert.Hosts...)
		if s.Has(host) {
			return true
		}
	}

	return false
}

func isUpstreamInKong(name string, upstreams []kong.Upstream) bool {
	for _, upstream := range upstreams {
		if upstream.Name == name {
			return true
		}
	}

	return false
}

func isServiceInKong(name string, services []kong.Service) bool {
	for _, svc := range services {
		if svc.Name == name {
			return true
		}
	}

	return false
}

// isRouteInKong checks if a route is already present in Kong
func isRouteInKong(route *kong.Route, routes []kong.Route) bool {
	for _, eRoute := range routes {
		if route.Equal(&eRoute) {
			return true
		}
	}

	return false
}

func deleteServiceUpstream(host string, client *kong.RestClient) error {
	kongUpstreams, err := client.Upstream().List(nil)
	if err != nil {
		return err
	}

	upstreamsToRemove := sets.NewString()
	for _, upstream := range kongUpstreams.Items {
		if upstream.Name == host {
			if !upstreamsToRemove.Has(upstream.ID) {
				upstreamsToRemove.Insert(upstream.ID)
			}
		}
	}

	for _, upstream := range upstreamsToRemove.List() {
		kongTargets, err := client.Target().List(nil, upstream)
		if err != nil {
			return err
		}

		for _, target := range kongTargets.Items {
			if target.Upstream == upstream {
				err := client.Target().Delete(target.ID, upstream)
				if err != nil {
					return err
				}
			}
		}

		err = client.Upstream().Delete(upstream)
		if err != nil {
			return err
		}
	}

	return nil
}
