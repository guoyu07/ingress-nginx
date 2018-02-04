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
	"net"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/ingress-nginx/internal/kong"

	"github.com/golang/glog"

	apiv1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	"k8s.io/client-go/kubernetes/scheme"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/kubernetes/pkg/util/filesystem"

	"k8s.io/ingress-nginx/internal/file"
	"k8s.io/ingress-nginx/internal/ingress"
	"k8s.io/ingress-nginx/internal/ingress/annotations"
	"k8s.io/ingress-nginx/internal/ingress/annotations/class"
	"k8s.io/ingress-nginx/internal/ingress/controller/store"
	"k8s.io/ingress-nginx/internal/ingress/status"
	ing_net "k8s.io/ingress-nginx/internal/net"
	"k8s.io/ingress-nginx/internal/net/dns"
	"k8s.io/ingress-nginx/internal/task"
)

// NewNGINXController creates a new NGINX Ingress controller.
// If the environment variable NGINX_BINARY exists it will be used
// as source for nginx commands
func NewNGINXController(config *Configuration, fs file.Filesystem) *NGINXController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{
		Interface: config.Client.CoreV1().Events(config.Namespace),
	})

	h, err := dns.GetSystemNameServers()
	if err != nil {
		glog.Warningf("unexpected error reading system nameservers: %v", err)
	}

	n := &NGINXController{
		isIPV6Enabled: ing_net.IsIPv6Enabled(),

		resolver:        h,
		cfg:             config,
		syncRateLimiter: flowcontrol.NewTokenBucketRateLimiter(config.SyncRateLimit, 1),

		recorder: eventBroadcaster.NewRecorder(scheme.Scheme, apiv1.EventSource{
			Component: "nginx-ingress-controller",
		}),

		stopCh:   make(chan struct{}),
		updateCh: make(chan store.Event, 1024),

		stopLock: &sync.Mutex{},

		fileSystem: fs,

		// create an empty configuration.
		runningConfig: &ingress.Configuration{},
	}

	n.store = store.New(
		config.EnableSSLChainCompletion,
		config.Namespace,
		config.ConfigMapName,
		"",
		"",
		"",
		config.ResyncPeriod,
		config.Client,
		fs,
		n.updateCh)

	n.syncQueue = task.NewTaskQueue(n.syncIngress)

	n.annotations = annotations.NewAnnotationExtractor(n.store)

	if config.UpdateStatus {
		n.syncStatus = status.NewStatusSyncer(status.Config{
			Client:                 config.Client,
			PublishService:         config.PublishService,
			IngressLister:          n.store,
			ElectionID:             config.ElectionID,
			IngressClass:           class.IngressClass,
			DefaultIngressClass:    class.DefaultClass,
			UpdateStatusOnShutdown: config.UpdateStatusOnShutdown,
			UseNodeInternalIP:      config.UseNodeInternalIP,
		})
	} else {
		glog.Warning("Update of ingress status is disabled (flag --update-status=false was specified)")
	}

	return n
}

// NGINXController ...
type NGINXController struct {
	cfg *Configuration

	annotations annotations.Extractor

	recorder record.EventRecorder

	syncQueue *task.Queue

	syncStatus status.Sync

	syncRateLimiter flowcontrol.RateLimiter

	// stopLock is used to enforce only a single call to Stop is active.
	// Needed because we allow stopping through an http endpoint and
	// allowing concurrent stoppers leads to stack traces.
	stopLock *sync.Mutex

	stopCh   chan struct{}
	updateCh chan store.Event

	// ngxErrCh channel used to detect errors with the nginx processes
	ngxErrCh chan error

	// runningConfig contains the running configuration in the Backend
	runningConfig *ingress.Configuration

	resolver []net.IP

	// returns true if IPV6 is enabled in the pod
	isIPV6Enabled bool

	isShuttingDown bool

	store store.Storer

	fileSystem filesystem.Filesystem
}

// Start start a new NGINX master process running in foreground.
func (n *NGINXController) Start() {
	glog.Infof("starting Ingress controller")

	n.store.Run(n.stopCh)

	if n.syncStatus != nil {
		go n.syncStatus.Run()
	}

	done := make(chan error, 1)

	go n.syncQueue.Run(time.Second, n.stopCh)
	// force initial sync
	n.syncQueue.Enqueue(&extensions.Ingress{})

	for {
		select {
		case err := <-done:
			if n.isShuttingDown {
				break
			}
			glog.Infof("Unexpected error: %v", err)
		case evt := <-n.updateCh:
			if n.isShuttingDown {
				break
			}
			glog.V(3).Infof("Event %v received - object %v", evt.Type, evt.Obj)
			n.syncQueue.Enqueue(evt.Obj)
		case <-n.stopCh:
			break
		}
	}
}

// Stop gracefully stops the NGINX master process.
func (n *NGINXController) Stop() error {
	n.isShuttingDown = true

	n.stopLock.Lock()
	defer n.stopLock.Unlock()

	// Only try draining the workqueue if we haven't already.
	if n.syncQueue.IsShuttingDown() {
		return fmt.Errorf("shutdown already in progress")
	}

	glog.Infof("shutting down controller queues")
	close(n.stopCh)
	go n.syncQueue.Shutdown()
	if n.syncStatus != nil {
		n.syncStatus.Shutdown()
	}

	return nil
}

// DefaultEndpoint returns the default endpoint to be use as default server that returns 404.
func (n NGINXController) DefaultEndpoint() ingress.Endpoint {
	return ingress.Endpoint{
		Address: "127.0.0.1",
		Port:    fmt.Sprintf("%v", 8181),
		Target:  &apiv1.ObjectReference{},
	}
}

// OnUpdate is called periodically by syncQueue to keep the configuration in sync.
//
// 1. converts configmap configuration to custom configuration object
// 2. write the custom template (the complexity depends on the implementation)
// 3. write the configuration file
//
// returning nill implies the backend will be reloaded.
// if an error is returned means requeue the update
func (n *NGINXController) OnUpdate(ingressCfg ingress.Configuration) error {

	kongUpstreams, err := n.cfg.KongClient.Upstream().List(nil)
	if err != nil {
		return err
	}

	kongCertificates, err := n.cfg.KongClient.Certificate().List(nil)
	if err != nil {
		return err
	}

	// Create Kong Upstreams and Targets
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			continue
		}

		if server.SSLCertificate != "" {
			// check the certificate is present in kong
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

					kongUpstreamName := server.Hostname
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

						kongUpstreams.Items = append(kongUpstreams.Items, *u)
					}

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

	kongServices, err := n.cfg.KongClient.Services().List(nil)
	if err != nil {
		return err
	}

	// Check if the endpoints exists as a service in kong
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
			continue
		}

		for _, location := range server.Locations {
			backend := location.Backend
			for _, upstream := range ingressCfg.Backends {
				proto := "http"
				if upstream.Secure {
					proto = "https"
				}

				if upstream.Name == backend {
					name := buildName(backend, location)
					if !isServiceInKong(name, kongServices.Items) {
						s := &kong.Service{
							Name:           name,
							Path:           "/",
							Protocol:       proto,
							Host:           server.Hostname,
							Port:           80,
							ConnectTimeout: location.Proxy.ConnectTimeout,
							ReadTimeout:    location.Proxy.ReadTimeout,
							WriteTimeout:   location.Proxy.SendTimeout,
							Retries:        5,
						}
						glog.Infof("creating Kong Service name %v", name)
						_, res := n.cfg.KongClient.Services().Create(s)
						if res.StatusCode != 201 {
							glog.Errorf("Unexpected error creating Kong Service: %v", res.Error())
							return res.Error()
						}
					}

					break
				}
			}
		}
	}

	kongServices, err = n.cfg.KongClient.Services().List(nil)
	if err != nil {
		return err
	}

	kongRoutes, err := n.cfg.KongClient.Routes().List(nil)
	if err != nil {
		return err
	}

	// Routes
	for _, server := range ingressCfg.Servers {
		if server.Hostname == "_" {
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
			if backend == "" {
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

func buildName(backend string, location *ingress.Location) string {
	r := strings.Replace(location.Path, "/", "-", -1)
	return strings.TrimSuffix(fmt.Sprintf("%v%v", backend, r), "-")
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

func isRouteInKong(route *kong.Route, routes []kong.Route) bool {
	for _, eRoute := range routes {
		if route.Equal(&eRoute) {
			return true
		}
	}

	return false
}
