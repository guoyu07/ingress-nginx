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

	// We need to synchronizde the state between Kubernetes and Kong
	// Steps:
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
	// For instance an Ingress, a Service or a Secret is removed

	// Note: returning an error in the sync loop means retry.

	kongUpstreams, err := n.cfg.KongClient.Upstream().List(nil)
	if err != nil {
		return err
	}

	kongCertificates, err := n.cfg.KongClient.Certificate().List(nil)
	if err != nil {
		return err
	}

	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			// there is no catch all server in kong
			continue
		}

		// check the certificate is present in kong
		if server.SSLCertificate != "" {
			if !isCertificateInKong(server.Hostname, kongCertificates.Items) {
				sc := bytes.NewBuffer(server.SSLCert.Raw.Cert).String()
				sk := bytes.NewBuffer(server.SSLCert.Raw.Key).String()
				cert := &kong.Certificate{
					Cert:  sc,
					Key:   sk,
					Hosts: []string{server.Hostname},
				}
				glog.Infof("creating Kong SSL Certificate for host %v", server.Hostname)
				cert, res := n.cfg.KongClient.Certificate().Create(cert)
				if res.StatusCode != 201 {
					glog.Errorf("Unexpected error creating Kong Certificate: %v", res.Error())
					return res.Error()
				}
				kongCertificates.Items = append(kongCertificates.Items, *cert)

				sni := &kong.SNI{
					Name:        server.Hostname,
					Certificate: cert.ID,
				}
				glog.Infof("creating Kong SNI for host %v and certificate id %v", server.Hostname, cert.ID)
				_, res = n.cfg.KongClient.SNI().Create(sni)
				if res.StatusCode != 201 {
					glog.Errorf("Unexpected error creating Kong SNI: %v", res.Error())
					return res.Error()
				}
			}
		}

		for _, location := range server.Locations {
			backend := location.Backend
			if backend == "default-backend" {
				// there is no default backend in Kong
				continue
			}

			for _, upstream := range ingressCfg.Backends {
				if upstream.Name == backend {
					var b *kong.Upstream
					kongUpstreamName := buildName(backend, location)

					// upstream sync block
					{
						if !isUpstreamInKong(kongUpstreamName, kongUpstreams.Items) {
							upstream := &kong.Upstream{
								Name: kongUpstreamName,
							}
							glog.Infof("creating Kong Upstream with name %v", kongUpstreamName)
							u, res := n.cfg.KongClient.Upstream().Create(upstream)
							if res.StatusCode != 201 {
								glog.Errorf("Unexpected error creating Kong Upstream: %v", res.Error())
								return res.Error()
							}

							b = u
							// Add the new upstream to avoid new requests
							kongUpstreams.Items = append(kongUpstreams.Items, *u)
						}
					}

					// sync target block
					{
						b = getKongUpstream(kongUpstreamName, kongUpstreams.Items)
						if b == nil {
							glog.Errorf("there is no upstream for hostname %v", kongUpstreamName)
							return nil
						}

						// reconcile the state between the ingress controller and kong comparing
						// the endpoints in Kubernetes and the targets in the kong upstream.
						// To avoid downtimes we create the new targets first and then remove
						// the killed ones.
						kongTargets, err := n.cfg.KongClient.Target().List(nil, kongUpstreamName)
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
						for _, endpoint := range upstream.Endpoints {
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
							_, res := n.cfg.KongClient.Target().Create(target, kongUpstreamName)
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
								glog.Infof("deleting Kong Target %v", kongTarget)
								err := n.cfg.KongClient.Target().Delete(kongTarget.ID, b.ID)
								if err != nil {
									glog.Errorf("Unexpected error deleting Kong Upstream: %v", err)
									return err
								}
							}
						}
					}
				}
			}
		}
	}

	kongServices, err := n.cfg.KongClient.Services().List(nil)
	if err != nil {
		return err
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
					svc, res := n.cfg.KongClient.Services().Create(s)
					if res.StatusCode != 201 {
						glog.Errorf("Unexpected error creating Kong Service: %v", res.Error())
						return res.Error()
					}

					kongServices.Items = append(kongServices.Items, *svc)
				}

				break
			}
		}
	}

	kongRoutes, err := n.cfg.KongClient.Routes().List(nil)
	if err != nil {
		return err
	}

	// Routes
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			// there is no catch all server in kong
			continue
		}

		glog.Infof("Configuring routes for server %v", server.Hostname)
		routeUpdated := false

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
				glog.Warningf("there is no Kong service with name %v", name)
				continue
			}

			r := &kong.Route{
				Paths:     []string{location.Path},
				Protocols: protos,
				Hosts:     []string{server.Hostname},
				Service:   kong.InlineService{ID: svc.ID},
			}
			if !isRouteInKong(r, kongRoutes.Items) {
				routeUpdated = true
				glog.Infof("creating Kong Route for host %v, path %v and service %v", server.Hostname, location.Path, svc.ID)
				r, res := n.cfg.KongClient.Routes().Create(r)
				if res.StatusCode != 201 {
					glog.Errorf("Unexpected error creating Kong Route: %v", res.Error())
					return res.Error()
				}

				kongRoutes.Items = append(kongRoutes.Items, *r)
			} else {
				// the route could exists but the protocols could be different (we are adding ssl)
				route := getKongRoute(server.Hostname, location.Path, kongRoutes.Items)

				for _, proto := range protos {
					found := false
					for _, rp := range route.Protocols {
						if proto == rp {
							found = true
							break
						}
					}
					if !found {
						route.Protocols = protos
						_, res := n.cfg.KongClient.Routes().Patch(route)
						if res.StatusCode != 201 {
							glog.Errorf("Unexpected error updating Kong Route: %v", res.Error())
							return res.Error()
						}
					}
				}
			}
		}

		if !routeUpdated {
			glog.Infof("there is no route updates to server %v", server.Hostname)
		}
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
